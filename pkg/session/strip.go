package session

import "strings"

// StripState holds state for ANSI stripping across chunked reads.
// This allows proper tracking of alternate screen buffer mode even when
// the enter/leave sequences are split across multiple Read() calls.
type StripState struct {
	InAltScreen bool
}

// Strip removes ANSI escape sequences and control characters from s.
// It is state-aware: when an alternate screen buffer enter sequence is detected
// (ESC[?1049h, ESC[?47h, ESC[?1047h), all subsequent content is discarded until
// the corresponding leave sequence (ESC[?1049l, ESC[?47l, ESC[?1047l) is seen.
// This correctly handles TUI apps (kiro-cli, claude) whose chrome should be dropped.
func (st *StripState) Strip(s string) string {
	var result strings.Builder
	if !st.InAltScreen {
		result.Grow(len(s) / 2)
	}

	i := 0
	for i < len(s) {
		if s[i] == 0x1B {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI sequence
				i++
				// Capture parameter bytes to detect alt screen sequences
				paramStart := i
				for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3F {
					i++
				}
				paramEnd := i
				// Skip intermediate bytes (0x20–0x2F)
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2F {
					i++
				}
				// Final byte (0x40–0x7E) — the command letter
				var finalByte byte
				if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7E {
					finalByte = s[i]
					i++
				}
				// Check for alt screen enter/leave
				params := s[paramStart:paramEnd]
				if finalByte == 'h' && isAltScreenParam(params) {
					st.InAltScreen = true
				} else if finalByte == 'l' && isAltScreenParam(params) {
					st.InAltScreen = false
				}

			case ']': // OSC — Operating System Command
				i++
				for i < len(s) {
					if s[i] == 0x07 { // BEL terminates
						i++
						break
					}
					if s[i] == 0x1B && i+1 < len(s) && s[i+1] == '\\' { // ST terminates
						i += 2
						break
					}
					i++
				}

			case 'P', 'X', '^', '_': // DCS, SOS, PM, APC
				i++
				for i < len(s) {
					if s[i] == 0x1B && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					if s[i] == 0x9C { // single-byte ST (C1)
						i++
						break
					}
					i++
				}

			case '(', ')', '*', '+': // Character set designation (G0–G3)
				i++
				if i < len(s) {
					i++ // skip the charset designator byte
				}

			case '#': // DEC line attributes (e.g. ESC#8 = DECALN)
				i++
				if i < len(s) {
					i++
				}

			default:
				// Two-char ESC sequences: save/restore cursor (7/8), RIS (c),
				// DECPNM (>), DECPAM (=), NEL (E), RI (M), HTS (H), etc.
				i++
			}
		} else if s[i] == 0x9B {
			// Single-byte CSI (C1 control, 0x9B) — same as ESC[
			i++
			paramStart := i
			for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3F {
				i++
			}
			paramEnd := i
			for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2F {
				i++
			}
			var finalByte byte
			if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7E {
				finalByte = s[i]
				i++
			}
			// Check for alt screen via single-byte CSI too
			params := s[paramStart:paramEnd]
			if finalByte == 'h' && isAltScreenParam(params) {
				st.InAltScreen = true
			} else if finalByte == 'l' && isAltScreenParam(params) {
				st.InAltScreen = false
			}

		} else if s[i] == 0x90 || s[i] == 0x98 || s[i] == 0x9E || s[i] == 0x9F {
			// Single-byte DCS/SOS/PM/APC (C1 range)
			i++
			for i < len(s) && s[i] != 0x9C {
				i++
			}
			if i < len(s) {
				i++ // skip ST
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
		} else {
			// Only emit content when NOT inside alternate screen buffer
			if !st.InAltScreen {
				result.WriteByte(s[i])
			}
			i++
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
