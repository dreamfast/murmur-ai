package irc

import (
	"testing"
)

func TestMarkdownToIRC_Bold(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("this is **bold** text")
	want := "this is \x02bold\x02 text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Italic(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("this is *italic* text")
	want := "this is \x1Ditalic\x1D text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_BoldItalic(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("this is ***bold italic*** text")
	want := "this is \x02\x1Dbold italic\x0F text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_InlineCode(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("run `go test` now")
	want := "run \x0307go test\x0F now"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Strikethrough(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("this is ~~deleted~~ text")
	want := "this is \x0314deleted\x0F text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Link(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("check [this](https://example.com) out")
	want := "check \x1Fthis\x1F (\x0311https://example.com\x0F) out"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Heading1(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("# Main Title")
	want := "\x02\x0312Main Title\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Heading2(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("## Section")
	want := "\x02\x0310Section\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Heading3(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("### Subsection")
	want := "\x02Subsection\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_BulletList(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("- item one\n- item two")
	want := "\x0303* \x0Fitem one\n\x0303* \x0Fitem two"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_NumberedList(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("1. first\n2. second")
	want := "\x03031. \x0Ffirst\n\x03032. \x0Fsecond"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_Blockquote(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("> quoted text")
	want := "\x0314| quoted text\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_CodeBlock(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("```python\nprint('hello')\nx = 42\n```")
	want := "\x0315[python]\x0F\n\x0315print('hello')\x0F\n\x0315x = 42\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_CodeBlockNoLang(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("```\nsome code\n```")
	want := "\x0315some code\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_HorizontalRule(t *testing.T) {
	t.Parallel()
	got := MarkdownToIRC("---")
	want := "\x0314---\x0F"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToIRC_PlainText(t *testing.T) {
	t.Parallel()
	input := "just plain text with no formatting"
	got := MarkdownToIRC(input)
	if got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}

func TestMarkdownToIRC_Mixed(t *testing.T) {
	t.Parallel()
	input := "## Status\n\n- **CPU**: 45%\n- **Memory**: `2.1GB` / 8GB\n\n> All systems normal"
	got := MarkdownToIRC(input)
	// Just verify it doesn't panic and contains IRC codes.
	if len(got) == 0 {
		t.Error("expected non-empty output")
	}
	// Should contain bold code.
	if !containsByte(got, 0x02) {
		t.Error("expected bold formatting code")
	}
	// Should contain color code.
	if !containsByte(got, 0x03) {
		t.Error("expected color formatting code")
	}
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
