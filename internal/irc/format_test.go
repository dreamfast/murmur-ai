package irc

import (
	"strings"
	"testing"
)

func TestSplitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		msg    string
		maxLen int
		want   []string
	}{
		{
			name:   "empty string returns nil",
			msg:    "",
			maxLen: 100,
			want:   nil,
		},
		{
			name:   "single line under limit",
			msg:    "hello world",
			maxLen: 100,
			want:   []string{"hello world"},
		},
		{
			name:   "single line over limit splits at word boundary",
			msg:    "hello world",
			maxLen: 7,
			want:   []string{"hello", "world"},
		},
		{
			name:   "multi-line input splits on newlines",
			msg:    "line one\nline two\nline three",
			maxLen: 100,
			want:   []string{"line one", "line two", "line three"},
		},
		{
			name:   "blank lines are skipped",
			msg:    "first\n\n\nsecond",
			maxLen: 100,
			want:   []string{"first", "second"},
		},
		{
			name:   "maxLen <= 0 uses default MaxMessageLen",
			msg:    "short",
			maxLen: 0,
			want:   []string{"short"},
		},
		{
			name:   "negative maxLen uses default MaxMessageLen",
			msg:    "short",
			maxLen: -5,
			want:   []string{"short"},
		},
		{
			name:   "very long word without spaces",
			msg:    strings.Repeat("a", 20),
			maxLen: 8,
			want:   []string{strings.Repeat("a", 8), strings.Repeat("a", 8), strings.Repeat("a", 4)},
		},
		{
			name:   "UTF-8 characters not split mid-rune",
			msg:    strings.Repeat("é", 10), // each é is 2 bytes
			maxLen: 5,                       // can't fit 3 runes (6 bytes), so should split at rune boundary
			want:   []string{strings.Repeat("é", 2), strings.Repeat("é", 2), strings.Repeat("é", 2), strings.Repeat("é", 2), strings.Repeat("é", 2)},
		},
		{
			name:   "whitespace-only string returns nil",
			msg:    "   \n  \n  ",
			maxLen: 100,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SplitMessage(tt.msg, tt.maxLen)
			if tt.want == nil {
				if got != nil {
					t.Errorf("SplitMessage(%q, %d) = %v, want nil", tt.msg, tt.maxLen, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("SplitMessage(%q, %d) returned %d chunks, want %d\ngot:  %v\nwant: %v",
					tt.msg, tt.maxLen, len(got), len(tt.want), got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("chunk[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStripFormatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "no formatting returns unchanged",
			msg:  "hello world",
			want: "hello world",
		},
		{
			name: "bold codes removed",
			msg:  "\x02bold text\x02",
			want: "bold text",
		},
		{
			name: "italic codes removed",
			msg:  "\x1Ditalic text\x1D",
			want: "italic text",
		},
		{
			name: "underline codes removed",
			msg:  "\x1Funderline text\x1F",
			want: "underline text",
		},
		{
			name: "reverse codes removed",
			msg:  "\x16reverse text\x16",
			want: "reverse text",
		},
		{
			name: "reset codes removed",
			msg:  "text\x0F more text",
			want: "text more text",
		},
		{
			name: "color with foreground only",
			msg:  "\x0304red text",
			want: "red text",
		},
		{
			name: "color with fg and bg",
			msg:  "\x0304,05colored text",
			want: "colored text",
		},
		{
			name: "mixed formatting",
			msg:  "\x02bold \x0304,05colored \x1Ditalic\x0F end",
			want: "bold colored italic end",
		},
		{
			name: "color code at end of string",
			msg:  "text\x0304",
			want: "text",
		},
		{
			name: "empty string",
			msg:  "",
			want: "",
		},
		{
			name: "color with single digit fg",
			msg:  "\x034red",
			want: "red",
		},
		{
			name: "color with single digit fg and bg",
			msg:  "\x034,5text",
			want: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := StripFormatting(tt.msg)
			if got != tt.want {
				t.Errorf("StripFormatting(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
