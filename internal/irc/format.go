package irc

import (
	"strings"
	"unicode/utf8"
)

// MaxMessageLen is the maximum length in bytes for a single IRC message line.
// IRC protocol allows 512 bytes total including prefix and CRLF, but practical
// limit for message content is around 400 bytes after accounting for protocol
// overhead (nick, user, host, command, channel name).
const MaxMessageLen = 400

// SplitMessage splits a message into chunks that each fit within maxLen bytes.
// It attempts to break at word boundaries where possible. If maxLen is <= 0,
// MaxMessageLen is used as the default.
//
// Messages containing newlines are first split on newlines, then each line
// is split by length if needed.
func SplitMessage(msg string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MaxMessageLen
	}

	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}

	// First split on newlines.
	rawLines := strings.Split(msg, "\n")

	var result []string
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		chunks := splitLine(line, maxLen)
		result = append(result, chunks...)
	}

	return result
}

// splitLine splits a single line (no newlines) into chunks of at most maxLen bytes.
func splitLine(line string, maxLen int) []string {
	if len(line) <= maxLen {
		return []string{line}
	}

	var chunks []string
	for len(line) > 0 {
		if len(line) <= maxLen {
			chunks = append(chunks, line)
			break
		}

		// Try to find a word boundary to break at.
		cutAt := maxLen
		spaceIdx := strings.LastIndex(line[:maxLen], " ")
		if spaceIdx > maxLen/4 {
			// Only use the space if it's not too close to the start.
			cutAt = spaceIdx
		} else {
			// No good word boundary — ensure we don't split a UTF-8 character.
			for cutAt > 0 && !utf8.RuneStart(line[cutAt]) {
				cutAt--
			}
			if cutAt == 0 {
				cutAt = maxLen
			}
		}

		chunks = append(chunks, strings.TrimSpace(line[:cutAt]))
		line = strings.TrimSpace(line[cutAt:])
	}

	return chunks
}

// StripFormatting removes IRC formatting codes (bold, color, underline, etc.)
// from a message string.
func StripFormatting(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))

	i := 0
	for i < len(msg) {
		ch := msg[i]
		switch ch {
		case 0x02, // Bold
			0x1D, // Italic
			0x1F, // Underline
			0x16, // Reverse
			0x0F: // Reset
			i++
		case 0x03: // Color
			i++
			// Skip up to 2 digits for foreground.
			for j := 0; j < 2 && i < len(msg) && msg[i] >= '0' && msg[i] <= '9'; j++ {
				i++
			}
			// Skip comma and up to 2 digits for background.
			if i < len(msg) && msg[i] == ',' {
				i++
				for j := 0; j < 2 && i < len(msg) && msg[i] >= '0' && msg[i] <= '9'; j++ {
					i++
				}
			}
		default:
			b.WriteByte(ch)
			i++
		}
	}

	return b.String()
}
