package session

import "testing"

func TestStripANSI_BasicSGR(t *testing.T) {
	input := "\x1b[1;32mhello\x1b[0m world"
	want := "hello world"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_CursorMovement(t *testing.T) {
	// ESC[H (home), ESC[5;10H (move to row 5 col 10), ESC[3A (up 3)
	input := "\x1b[Hfoo\x1b[5;10Hbar\x1b[3Abaz"
	want := "foobarbaz"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_EraseDisplay(t *testing.T) {
	// ESC[2J (erase entire display), ESC[K (erase to end of line)
	input := "\x1b[2Jhello\x1b[K world"
	want := "hello world"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_ScrollRegion(t *testing.T) {
	// ESC[5;20r (set scroll region), ESC[S (scroll up), ESC[2T (scroll down 2)
	input := "\x1b[5;20rfoo\x1b[Sbar\x1b[2Tbaz"
	want := "foobarbaz"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_OSCHyperlink(t *testing.T) {
	// OSC 8 hyperlink: ESC]8;;url BEL text ESC]8;; BEL
	input := "\x1b]8;;https://example.com\x07click here\x1b]8;;\x07"
	want := "click here"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_OSCTitle(t *testing.T) {
	// Window title: ESC]0;title ST(ESC\)
	input := "\x1b]0;my terminal\x1b\\hello"
	want := "hello"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_DCS(t *testing.T) {
	// DCS (ESC P ... ST)
	input := "\x1bPsome dcs data\x1b\\visible"
	want := "visible"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_PrivateCSI(t *testing.T) {
	// DEC private mode: ESC[?25l (hide cursor), ESC[?25h (show cursor)
	// These non-alt-screen private modes should just be stripped (content kept)
	input := "\x1b[?25lfoo\x1b[?25hbar"
	want := "foobar"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_AltScreen1049(t *testing.T) {
	// ESC[?1049h enters alt screen, content discarded, ESC[?1049l leaves
	input := "before\x1b[?1049hTUI chrome\x1b[?1049lafter"
	want := "beforeafter"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_AltScreen47(t *testing.T) {
	// ESC[?47h / ESC[?47l variant
	input := "hello\x1b[?47hdiscarded\x1b[?47l world"
	want := "hello world"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_AltScreen1047(t *testing.T) {
	// ESC[?1047h / ESC[?1047l variant
	input := "A\x1b[?1047hXXX\x1b[?1047lB"
	want := "AB"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripState_ChunkedAltScreen(t *testing.T) {
	// Alt screen enter and leave split across multiple reads
	st := &StripState{}

	// Chunk 1: enter alt screen midway
	r1 := st.Strip("before\x1b[?1049hTUI stuff")
	if r1 != "before" {
		t.Errorf("chunk1: got %q, want %q", r1, "before")
	}
	if !st.InAltScreen {
		t.Error("expected InAltScreen=true after chunk1")
	}

	// Chunk 2: still in alt screen, all discarded
	r2 := st.Strip("more TUI chrome\x1b[1;32mcolored")
	if r2 != "" {
		t.Errorf("chunk2: got %q, want %q", r2, "")
	}

	// Chunk 3: leave alt screen, content after is kept
	r3 := st.Strip("\x1b[?1049lafter exit")
	if r3 != "after exit" {
		t.Errorf("chunk3: got %q, want %q", r3, "after exit")
	}
	if st.InAltScreen {
		t.Error("expected InAltScreen=false after chunk3")
	}
}

func TestStripANSI_C1SingleByte(t *testing.T) {
	// 0x9B as single-byte CSI (rare but valid)
	input := "hello\x9b32mworld\x9b0m"
	want := "helloworld"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_MixedContent(t *testing.T) {
	// Real-world: color + cursor + erase + carriage return + newline
	input := "\x1b[2J\x1b[H\x1b[1;34m> \x1b[0mhello\r\nworld\x1b[K"
	want := "> hello\nworld"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_DEL(t *testing.T) {
	input := "abc\x7fdef"
	want := "abcdef"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_CharsetDesignation(t *testing.T) {
	// ESC(B (G0 to ASCII), ESC)0 (G1 to DEC Special Graphics)
	input := "\x1b(Bhello\x1b)0world"
	want := "helloworld"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripANSI_PlainText(t *testing.T) {
	input := "hello world\nline 2\ttab"
	want := "hello world\nline 2\ttab"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStripANSI_CJKNotCorrupted guards against treating UTF-8 continuation
// bytes as C1 control-code introducers. Each of these Traditional Chinese
// characters has a continuation byte that lands on 0x90/0x98/0x9B/0x9E/0x9F
// — the exact single-byte C1 values Strip() checks for.
func TestStripANSI_CJKNotCorrupted(t *testing.T) {
	cases := []string{
		"請幫我寫會議記錄",      // 記 = E8 A8 98 (continuation byte 0x98)
		"然後同步到 Notion",   // 同 = E5 90 8C (continuation byte 0x90)
		"整理架構文件",        // 架 = E6 9E B6 (continuation byte 0x9E)
		"換一種寫法",         // 換 = E6 8F 9B (continuation byte 0x9B)
	}
	for _, input := range cases {
		got := stripANSI(input)
		if got != input {
			t.Errorf("stripANSI(%q) = %q, want unchanged %q", input, got, input)
		}
	}
}

// TestStripANSI_CJKWithANSI checks that CJK text mixed with real ANSI
// sequences still strips the sequences correctly without corrupting the text.
func TestStripANSI_CJKWithANSI(t *testing.T) {
	input := "\x1b[1;32m會議記錄\x1b[0m：同步到 Notion 並整理架構文件"
	want := "會議記錄：同步到 Notion 並整理架構文件"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStripState_PartialAltScreenSequence covers the specific gap the code
// review found untested: a chunk boundary landing INSIDE an alt-screen
// enter/leave CSI sequence (not just a bare trailing ESC).
func TestStripState_PartialAltScreenSequence(t *testing.T) {
	st := &StripState{}

	// Enter sequence "\x1b[?1049h" split right after the params, before the
	// final byte.
	r1 := st.Strip("before\x1b[?104")
	if r1 != "before" {
		t.Errorf("chunk1: got %q, want %q", r1, "before")
	}
	if st.InAltScreen {
		t.Errorf("chunk1: InAltScreen should still be false (sequence incomplete)")
	}

	r2 := st.Strip("9hTUI content")
	if r2 != "" {
		t.Errorf("chunk2: got %q, want empty (now inside alt screen)", r2)
	}
	if !st.InAltScreen {
		t.Fatalf("chunk2: InAltScreen should be true after completed enter sequence")
	}

	// Leave sequence "\x1b[?1049l" split the same way.
	r3 := st.Strip("\x1b[?104")
	if r3 != "" {
		t.Errorf("chunk3: got %q, want empty", r3)
	}
	if !st.InAltScreen {
		t.Errorf("chunk3: InAltScreen should still be true (leave sequence incomplete)")
	}

	r4 := st.Strip("9lafter")
	if r4 != "after" {
		t.Errorf("chunk4: got %q, want %q", r4, "after")
	}
	if st.InAltScreen {
		t.Errorf("chunk4: InAltScreen should be false after completed leave sequence")
	}
}

// TestStripState_PartialEscapeKnownTwoChar ensures a bare trailing ESC that
// turns out (once reunited with the next chunk) to be a recognized two-char
// sequence like ESC 8 (DECRC) is still consumed as one, not leaked as text.
func TestStripState_PartialEscapeKnownTwoChar(t *testing.T) {
	st := &StripState{}
	r1 := st.Strip("hello\x1b")
	if r1 != "hello" {
		t.Errorf("chunk1: got %q, want %q", r1, "hello")
	}
	r2 := st.Strip("8world")
	if r2 != "world" {
		t.Errorf("chunk2: got %q, want %q (ESC 8 should be consumed, not leaked)", r2, "world")
	}
}
