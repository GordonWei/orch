package session

import (
	"strings"
	"unicode/utf8"
)

// StripState holds state for ANSI stripping across chunked reads.
// This allows proper tracking of alternate screen buffer mode even when
// the enter/leave sequences are split across multiple Read() calls, and
// buffers any escape sequence left incomplete at the end of a chunk so it
// can be completed once the next chunk of PTY output arrives.
type StripState struct {
	InAltScreen bool
	pending     string // bytes of an escape sequence truncated at the end of the previous Strip() call
}

// Strip removes ANSI escape sequences and control characters from s.
// It is state-aware: when an alternate screen buffer enter sequence is detected
// (ESC[?1049h, ESC[?47h, ESC[?1047h), all subsequent content is discarded until
// the corresponding leave sequence (ESC[?1049l, ESC[?47l, ESC[?1047l) is seen.
// This correctly handles TUI apps (kiro-cli, claude) whose chrome should be dropped.
//
// Escape sequences that are split across two Strip() calls (a chunk boundary
// lands mid-sequence) are buffered in st.pending and completed on the next call.
//
// High bytes (0x80-0xFF) are only treated as single-byte C1 control codes
// (0x9B/0x90/0x98/0x9E/0x9F) when they do not form part of a valid multi-byte
// UTF-8 sequence — otherwise a UTF-8 continuation byte belonging to an
// ordinary multi-byte character (e.g. CJK text) could be misidentified as a
// control code and corrupt the text. Valid multi-byte runes are passed
// through untouched.
func (st *StripState) Strip(s string) string {
	if st.pending != "" {
		s = st.pending + s
		st.pending = ""
	}

	var result strings.Builder
	if !st.InAltScreen {
		result.Grow(len(s) / 2)
	}

	i := 0
scan:
	for i < len(s) {
		if s[i] == 0x1B {
			seqStart := i
			i++
			if i >= len(s) {
				st.pending = s[seqStart:]
				break scan
			}
			switch s[i] {
			case '[': // CSI sequence
				i++
				paramStart := i
				for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3F {
					i++
				}
				paramEnd := i
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2F {
					i++
				}
				if i >= len(s) {
					st.pending = s[seqStart:]
					break scan
				}
				var finalByte byte
				if s[i] >= 0x40 && s[i] <= 0x7E {
					finalByte = s[i]
					i++
				}
				params := s[paramStart:paramEnd]
				if finalByte == 'h' && isAltScreenParam(params) {
					st.InAltScreen = true
				} else if finalByte == 'l' && isAltScreenParam(params) {
					st.InAltScreen = false
				}

			case ']': // OSC — Operating System Command
				i++
				terminated := false
				for i < len(s) {
					if s[i] == 0x07 { // BEL terminates
						i++
						terminated = true
						break
					}
					if s[i] == 0x1B && i+1 < len(s) && s[i+1] == '\\' { // ST terminates
						i += 2
						terminated = true
						break
					}
					if s[i] == 0x1B && i+1 >= len(s) {
						// Trailing ESC that might start a ST — need more data.
						break
					}
					i++
				}
				if !terminated {
					st.pending = s[seqStart:]
					break scan
				}

			case 'P', 'X', '^', '_': // DCS, SOS, PM, APC
				i++
				terminated := false
				for i < len(s) {
					if s[i] == 0x1B && i+1 < len(s) && s[i+1] == '\\' { // ST terminates
						i += 2
						terminated = true
						break
					}
					if s[i] == 0x9C { // single-byte ST (C1)
						i++
						terminated = true
						break
					}
					if s[i] == 0x1B && i+1 >= len(s) {
						break
					}
					i++
				}
				if !terminated {
					st.pending = s[seqStart:]
					break scan
				}

			case '(', ')', '*', '+': // Character set designation (G0–G3)
				i++
				if i >= len(s) {
					st.pending = s[seqStart:]
					break scan
				}
				i++ // skip the charset designator byte

			case '#': // DEC line attributes (e.g. ESC#8 = DECALN)
				i++
				if i >= len(s) {
					st.pending = s[seqStart:]
					break scan
				}
				i++

			default:
				// Two-char ESC sequences: save/restore cursor (7/8), RIS (c),
				// DECPNM (>), DECPAM (=), NEL (E), RI (M), HTS (H), IND (D),
				// DECID (Z), etc. Only consume the second byte when it's one
				// of these recognized identifiers — otherwise this ESC was
				// just stray/noise (e.g. a trailing ESC now reunited with
				// unrelated text via st.pending) and the byte must be left
				// for the next loop iteration to emit as ordinary text.
				switch s[i] {
				case '7', '8', 'c', '>', '=', 'E', 'M', 'H', 'D', 'Z':
					i++
				}
			}
		} else if s[i] == '\r' {
			// Strip carriage return (PTY artifact)
			i++
		} else if s[i] < 0x20 && s[i] != '\n' && s[i] != '\t' {
			// Strip all other C0 control characters (BEL, BS, VT, FF, etc.)
			i++
		} else if s[i] == 0x7F {
			// DEL character
			i++
		} else if s[i] < 0x80 {
			// Plain ASCII byte.
			if !st.InAltScreen {
				result.WriteByte(s[i])
			}
			i++
		} else {
			// High byte (0x80-0xFF). Decode as UTF-8 first: a valid multi-byte
			// rune (e.g. a CJK character) must be passed through untouched,
			// not inspected byte-by-byte for C1 control-code meaning.
			r, size := utf8.DecodeRuneInString(s[i:])
			if size > 1 && r != utf8.RuneError {
				if !st.InAltScreen {
					result.WriteString(s[i : i+size])
				}
				i += size
				continue
			}

			// Not a valid multi-byte UTF-8 sequence — treat as a raw byte
			// and check for single-byte (C1) control codes.
			switch {
			case s[i] == 0x9B: // Single-byte CSI (C1 control)
				seqStart := i
				i++
				paramStart := i
				for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3F {
					i++
				}
				paramEnd := i
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2F {
					i++
				}
				if i >= len(s) {
					st.pending = s[seqStart:]
					break scan
				}
				var finalByte byte
				if s[i] >= 0x40 && s[i] <= 0x7E {
					finalByte = s[i]
					i++
				}
				params := s[paramStart:paramEnd]
				if finalByte == 'h' && isAltScreenParam(params) {
					st.InAltScreen = true
				} else if finalByte == 'l' && isAltScreenParam(params) {
					st.InAltScreen = false
				}

			case s[i] == 0x90 || s[i] == 0x98 || s[i] == 0x9E || s[i] == 0x9F:
				// Single-byte DCS/SOS/PM/APC (C1 range)
				seqStart := i
				i++
				terminated := false
				for i < len(s) {
					if s[i] == 0x9C {
						i++
						terminated = true
						break
					}
					i++
				}
				if !terminated {
					st.pending = s[seqStart:]
					break scan
				}

			default:
				// Unrecognized high byte (not a known C1 control, not valid
				// UTF-8) — pass it through as-is rather than silently drop it.
				if !st.InAltScreen {
					result.WriteByte(s[i])
				}
				i++
			}
		}
	}
	return result.String()
}

// isAltScreenParam checks if the CSI parameter string corresponds to
// one of the alternate screen buffer private mode values.
// Matches: ?1049, ?47, ?1047
func isAltScreenParam(params string) bool {
	return params == "?1049" || params == "?47" || params == "?1047"
}

// stripANSI is a convenience wrapper for stateless, single-call stripping.
// It handles alternate screen detection within a single string but does NOT
// carry state across calls. Use StripState for chunked/streaming reads.
func stripANSI(s string) string {
	st := &StripState{}
	return st.Strip(s)
}
