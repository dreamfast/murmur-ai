package irc

import (
	"strings"
	"testing"
)

func TestSplitMessage_Short(t *testing.T) {
	t.Parallel()

	result := SplitMessage("hello world", MaxMessageLen)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if result[0] != "hello world" {
		t.Errorf("got %q, want %q", result[0], "hello world")
	}
}

func TestSplitMessage_Empty(t *testing.T) {
	t.Parallel()

	result := SplitMessage("", MaxMessageLen)
	if len(result) != 0 {
		t.Fatalf("expected 0 chunks for empty string, got %d", len(result))
	}
}

func TestSplitMessage_Whitespace(t *testing.T) {
	t.Parallel()

	result := SplitMessage("   \n  \n  ", MaxMessageLen)
	if len(result) != 0 {
		t.Fatalf("expected 0 chunks for whitespace-only string, got %d", len(result))
	}
}

func TestSplitMessage_Long(t *testing.T) {
	t.Parallel()

	// Create a message longer than maxLen with word boundaries.
	words := strings.Repeat("word ", 100) // 500 bytes
	result := SplitMessage(words, 50)

	for i, chunk := range result {
		if len(chunk) > 50 {
			t.Errorf("chunk %d exceeds maxLen: %d bytes", i, len(chunk))
		}
		if chunk == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}

	// Verify all content is preserved.
	joined := strings.Join(result, " ")
	if strings.TrimSpace(joined) != strings.TrimSpace(words) {
		t.Error("content was lost during splitting")
	}
}

func TestSplitMessage_NoSpaces(t *testing.T) {
	t.Parallel()

	// A very long word with no spaces — must be hard-split.
	long := strings.Repeat("a", 100)
	result := SplitMessage(long, 30)

	if len(result) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(result))
	}
	for i, chunk := range result {
		if len(chunk) > 30 {
			t.Errorf("chunk %d exceeds maxLen: %d bytes", i, len(chunk))
		}
	}

	// Verify all content is preserved.
	joined := strings.Join(result, "")
	if joined != long {
		t.Error("content was lost during hard-split")
	}
}

func TestSplitMessage_MultiLine(t *testing.T) {
	t.Parallel()

	msg := "line one\nline two\nline three"
	result := SplitMessage(msg, MaxMessageLen)

	if len(result) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %v", len(result), result)
	}
	if result[0] != "line one" {
		t.Errorf("chunk 0 = %q, want %q", result[0], "line one")
	}
	if result[1] != "line two" {
		t.Errorf("chunk 1 = %q, want %q", result[1], "line two")
	}
	if result[2] != "line three" {
		t.Errorf("chunk 2 = %q, want %q", result[2], "line three")
	}
}

func TestSplitMessage_MultiLineWithLong(t *testing.T) {
	t.Parallel()

	longLine := strings.Repeat("x ", 30) // 60 chars
	msg := "short\n" + longLine
	result := SplitMessage(msg, 40)

	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(result))
	}
	if result[0] != "short" {
		t.Errorf("chunk 0 = %q, want %q", result[0], "short")
	}
	for i, chunk := range result {
		if len(chunk) > 40 {
			t.Errorf("chunk %d exceeds maxLen: %d bytes", i, len(chunk))
		}
	}
}

func TestSplitMessage_DefaultMaxLen(t *testing.T) {
	t.Parallel()

	// maxLen <= 0 should use MaxMessageLen.
	result := SplitMessage("hello", 0)
	if len(result) != 1 || result[0] != "hello" {
		t.Errorf("unexpected result with maxLen=0: %v", result)
	}

	result = SplitMessage("hello", -1)
	if len(result) != 1 || result[0] != "hello" {
		t.Errorf("unexpected result with maxLen=-1: %v", result)
	}
}

func TestSplitMessage_Unicode(t *testing.T) {
	t.Parallel()

	// Ensure we don't split in the middle of a multi-byte UTF-8 character.
	// Each emoji is 4 bytes. With maxLen=10, we should split cleanly.
	msg := "🎉🎉🎉🎉🎉" // 5 emojis = 20 bytes
	result := SplitMessage(msg, 10)

	for i, chunk := range result {
		if len(chunk) > 10 {
			t.Errorf("chunk %d exceeds maxLen: %d bytes", i, len(chunk))
		}
		// Verify each chunk is valid UTF-8.
		for _, r := range chunk {
			if r == 0xFFFD {
				t.Errorf("chunk %d contains replacement character (broken UTF-8)", i)
			}
		}
	}
}

func TestStripFormatting_Bold(t *testing.T) {
	t.Parallel()

	input := "\x02bold text\x02"
	want := "bold text"
	got := StripFormatting(input)
	if got != want {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, want)
	}
}

func TestStripFormatting_Color(t *testing.T) {
	t.Parallel()

	input := "\x034red text\x03"
	want := "red text"
	got := StripFormatting(input)
	if got != want {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, want)
	}
}

func TestStripFormatting_ColorWithBackground(t *testing.T) {
	t.Parallel()

	input := "\x034,12red on blue\x03"
	want := "red on blue"
	got := StripFormatting(input)
	if got != want {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, want)
	}
}

func TestStripFormatting_Mixed(t *testing.T) {
	t.Parallel()

	input := "\x02\x034,12bold red\x03\x02 \x1Funderline\x1F \x0Freset"
	want := "bold red underline reset"
	got := StripFormatting(input)
	if got != want {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, want)
	}
}

func TestStripFormatting_NoFormatting(t *testing.T) {
	t.Parallel()

	input := "plain text"
	got := StripFormatting(input)
	if got != input {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, input)
	}
}

func TestStripFormatting_Empty(t *testing.T) {
	t.Parallel()

	got := StripFormatting("")
	if got != "" {
		t.Errorf("StripFormatting(\"\") = %q, want \"\"", got)
	}
}

func TestStripFormatting_ItalicAndReverse(t *testing.T) {
	t.Parallel()

	input := "\x1Ditalic\x1D \x16reverse\x16"
	want := "italic reverse"
	got := StripFormatting(input)
	if got != want {
		t.Errorf("StripFormatting(%q) = %q, want %q", input, got, want)
	}
}
