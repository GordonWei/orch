package session

import (
	"strings"
	"testing"
)

// TestStripState_RealWorldClaude simulates a real claude CLI session output
// including the TUI mode enter/exit pattern observed in practice.
func TestStripState_RealWorldClaude(t *testing.T) {
	st := &StripState{}

	// Claude initially outputs some ANSI setup (clearing screen, setting title)
	chunk1 := "\x1b]0;claude\x07\x1b[?25l\x1b[?1049h\x1b[H\x1b[2J"
	r1 := st.Strip(chunk1)
	if r1 != "" {
		t.Errorf("TUI setup should be empty, got %q", r1)
	}
	if !st.InAltScreen {
		t.Fatal("should be in alt screen after ESC[?1049h")
	}

	// Claude TUI draws its interface (all should be discarded)
	chunk2 := "\x1b[1;1H\x1b[1;34m╭──────────────────────────╮\x1b[0m\n" +
		"\x1b[2;1H\x1b[1;34m│\x1b[0m Claude Code             \x1b[1;34m│\x1b[0m\n" +
		"\x1b[3;1H\x1b[1;34m╰──────────────────────────╯\x1b[0m\n" +
		"\x1b[5;1H> "
	r2 := st.Strip(chunk2)
	if r2 != "" {
		t.Errorf("TUI chrome should be empty, got %q", r2)
	}

	// Claude exits alt screen and outputs the actual response
	chunk3 := "\x1b[?1049l\x1b[?25h\nHello! I'm Claude. How can I help you today?\n"
	r3 := st.Strip(chunk3)
	want3 := "\nHello! I'm Claude. How can I help you today?\n"
	if r3 != want3 {
		t.Errorf("real output: got %q, want %q", r3, want3)
	}
	if st.InAltScreen {
		t.Error("should NOT be in alt screen after ESC[?1049l")
	}
}

// TestStripState_RealWorldKiro simulates kiro-cli output pattern.
func TestStripState_RealWorldKiro(t *testing.T) {
	st := &StripState{}

	// Kiro startup: may use ESC[?47h variant
	chunk1 := "\x1b[?47h\x1b[H\x1b[2J\x1b[1;36mKiro CLI v2.1.0\x1b[0m\n\x1b[?47l"
	r1 := st.Strip(chunk1)
	// The banner "Kiro CLI v2.1.0" is inside alt screen, so discarded
	if r1 != "" {
		t.Errorf("alt screen content should be empty, got %q", r1)
	}

	// After leaving alt screen, normal output passes through
	chunk2 := "Ready. Working directory: ~/Desktop/Cowork\n"
	r2 := st.Strip(chunk2)
	if r2 != chunk2 {
		t.Errorf("normal output: got %q, want %q", r2, chunk2)
	}
}

// TestStripState_InterleavedAltScreen simulates multiple alt screen
// enter/exits (e.g., a session that shows a progress TUI then outputs results).
func TestStripState_InterleavedAltScreen(t *testing.T) {
	st := &StripState{}

	// First response: normal text
	r1 := st.Strip("Result 1: OK\n")
	if r1 != "Result 1: OK\n" {
		t.Errorf("r1: got %q", r1)
	}

	// Progress spinner enters alt screen
	r2 := st.Strip("\x1b[?1049h\x1b[H⠋ Working...\x1b[2;1H\x1b[90m(23%)\x1b[0m")
	if r2 != "" {
		t.Errorf("spinner should be empty, got %q", r2)
	}

	// Progress continues
	r3 := st.Strip("\x1b[1;1H⠙ Working...\x1b[2;1H\x1b[90m(67%)\x1b[0m")
	if r3 != "" {
		t.Errorf("progress should be empty, got %q", r3)
	}

	// Done, exits alt screen, shows result
	r4 := st.Strip("\x1b[?1049lResult 2: 42 files processed\n")
	if r4 != "Result 2: 42 files processed\n" {
		t.Errorf("r4: got %q", r4)
	}
}

// TestStripState_SessionOutput_Pipeline tests the full pipeline:
// readLoop collects output -> StripState filters -> only meaningful text remains.
func TestStripState_SessionOutput_Pipeline(t *testing.T) {
	st := &StripState{}
	var output strings.Builder

	// Simulate multiple readLoop iterations (as would happen in session.go)
	chunks := []string{
		// Startup noise (alt screen)
		"\x1b[?1049h\x1b[H\x1b[2J\x1b[1;32mWelcome\x1b[0m",
		// More TUI drawing
		"\x1b[5;1H> \x1b[?25h",
		// Exit alt screen + actual response
		"\x1b[?1049l\x1b[?25h",
		"The answer to your question is:\n",
		"1. First point\n",
		"2. Second point\n",
	}

	for _, chunk := range chunks {
		cleaned := st.Strip(chunk)
		if strings.TrimSpace(cleaned) != "" {
			output.WriteString(cleaned)
		}
	}

	want := "The answer to your question is:\n1. First point\n2. Second point\n"
	got := output.String()
	if got != want {
		t.Errorf("pipeline output:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestStripState_NoFalsePositive ensures normal content without alt screen
// sequences passes through unmodified.
func TestStripState_NoFalsePositive(t *testing.T) {
	st := &StripState{}

	// Normal output with SGR colors (no alt screen)
	input := "\x1b[1;32m✅ success\x1b[0m: deployed to production\n"
	want := "✅ success: deployed to production\n"
	got := st.Strip(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStripState_PartialEscape ensures that a truncated ESC at the end of
// a chunk doesn't corrupt state or cause panics.
func TestStripState_PartialEscape(t *testing.T) {
	st := &StripState{}

	// Chunk ends with bare ESC (incomplete sequence)
	r1 := st.Strip("hello\x1b")
	if r1 != "hello" {
		t.Errorf("partial ESC: got %q, want %q", r1, "hello")
	}

	// Next chunk continues normally
	r2 := st.Strip("world\n")
	if r2 != "world\n" {
		t.Errorf("after partial: got %q, want %q", r2, "world\n")
	}
}
