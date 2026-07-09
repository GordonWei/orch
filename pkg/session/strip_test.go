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
	// ESC[?1049h (alt screen buffer), ESC[?1049l (restore)
	input := "\x1b[?25lfoo\x1b[?25h\x1b[?1049hbar\x1b[?1049l"
	want := "foobar"
	got := stripANSI(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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
