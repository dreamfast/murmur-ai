package irc

import (
	"regexp"
	"strings"
)

// IRC formatting control codes.
const (
	ircBold      = "\x02"
	ircItalic    = "\x1D"
	ircUnderline = "\x1F"
	ircColor     = "\x03"
	ircReset     = "\x0F"
)

// IRC color codes.
const (
	colorGreen     = "03"
	colorOrange    = "07"
	colorCyan      = "10"
	colorLightCyan = "11"
	colorLightBlue = "12"
	colorGrey      = "14"
	colorLightGrey = "15"
)

// Regex patterns for markdown elements.
var (
	// Inline patterns -- order matters (bold+italic before bold before italic).
	reBoldItalic  = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic      = regexp.MustCompile(`\*(.+?)\*`)
	reUnderItalic = regexp.MustCompile(`_(.+?)_`)
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeading     = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	reListBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+(.+)$`)
	reListNumber  = regexp.MustCompile(`^(\s*)\d+\.\s+(.+)$`)
	reCodeBlock   = regexp.MustCompile("^```")
	reBlockquote  = regexp.MustCompile(`^>\s*(.*)$`)
	reHR          = regexp.MustCompile(`^[-*_]{3,}\s*$`)
)

// MarkdownToIRC converts markdown-formatted text to IRC formatting codes.
// It handles bold, italic, underline, strikethrough, inline code, code blocks,
// headings, links, lists, blockquotes, and horizontal rules.
func MarkdownToIRC(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	codeBlockLang := ""

	for _, line := range lines {
		// Handle code blocks.
		if reCodeBlock.MatchString(line) {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockLang = strings.TrimPrefix(strings.TrimSpace(line), "```")
				if codeBlockLang != "" {
					result = append(result, ircColor+colorLightGrey+"["+codeBlockLang+"]"+ircReset)
				}
				continue
			}
			// Closing code block.
			inCodeBlock = false
			codeBlockLang = ""
			continue
		}

		if inCodeBlock {
			// Code block content -- grey color, preserve as-is.
			result = append(result, ircColor+colorLightGrey+line+ircReset)
			continue
		}

		line = convertLine(line)
		result = append(result, line)
	}

	// If code block was never closed, that's fine -- we already formatted the lines.
	_ = codeBlockLang

	return strings.Join(result, "\n")
}

// convertLine converts a single non-code-block line from markdown to IRC formatting.
func convertLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// Horizontal rule.
	if reHR.MatchString(trimmed) {
		return ircColor + colorGrey + "---" + ircReset
	}

	// Headings: ## Title -> bold + colored.
	if m := reHeading.FindStringSubmatch(trimmed); m != nil {
		level := len(m[1])
		title := convertInline(m[2])
		switch {
		case level == 1:
			return ircBold + ircColor + colorLightBlue + title + ircReset
		case level == 2:
			return ircBold + ircColor + colorCyan + title + ircReset
		default:
			return ircBold + title + ircReset
		}
	}

	// Blockquote: > text -> grey with bar.
	if m := reBlockquote.FindStringSubmatch(trimmed); m != nil {
		content := convertInline(m[1])
		return ircColor + colorGrey + "| " + content + ircReset
	}

	// Bullet list: - item -> colored bullet.
	if m := reListBullet.FindStringSubmatch(line); m != nil {
		indent := m[1]
		content := convertInline(m[2])
		return indent + ircColor + colorGreen + "* " + ircReset + content
	}

	// Numbered list: 1. item -> colored number.
	if m := reListNumber.FindStringSubmatch(line); m != nil {
		indent := m[1]
		content := convertInline(m[2])
		// Extract the original number.
		numEnd := strings.Index(strings.TrimSpace(line), ".")
		num := strings.TrimSpace(line)[:numEnd]
		return indent + ircColor + colorGreen + num + ". " + ircReset + content
	}

	// Regular line -- just convert inline formatting.
	return convertInline(line)
}

// convertInline converts inline markdown formatting to IRC codes.
func convertInline(text string) string {
	// Links: [text](url) -> text (underlined) + url in grey.
	text = reLink.ReplaceAllStringFunc(text, func(match string) string {
		m := reLink.FindStringSubmatch(match)
		return ircUnderline + m[1] + ircUnderline + " (" + ircColor + colorLightCyan + m[2] + ircReset + ")"
	})

	// Bold+italic: ***text*** -> bold+italic.
	text = reBoldItalic.ReplaceAllString(text, ircBold+ircItalic+"${1}"+ircReset)

	// Bold: **text** -> bold.
	text = reBold.ReplaceAllString(text, ircBold+"${1}"+ircBold)

	// Italic: *text* -> italic (but not inside words like file*name).
	text = reItalic.ReplaceAllString(text, ircItalic+"${1}"+ircItalic)

	// Underline italic: _text_ -> italic.
	text = reUnderItalic.ReplaceAllString(text, ircItalic+"${1}"+ircItalic)

	// Strikethrough: ~~text~~ -> grey (IRC has no native strikethrough).
	text = reStrike.ReplaceAllString(text, ircColor+colorGrey+"${1}"+ircReset)

	// Inline code: `code` -> orange on grey background.
	text = reInlineCode.ReplaceAllString(text, ircColor+colorOrange+"${1}"+ircReset)

	return text
}
