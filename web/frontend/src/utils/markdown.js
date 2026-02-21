/**
 * Lightweight markdown renderer for bot messages.
 *
 * Converts a subset of Markdown to HTML for display in the chat.
 * Supports:
 *   - Code blocks (``` ... ```) with syntax highlighting class
 *   - Inline code (`code`)
 *   - Bold (**text** or __text__)
 *   - Italic (*text* or _text_)
 *   - Strikethrough (~~text~~)
 *   - Links (https://... auto-linked, [text](url))
 *   - Unordered lists (- item, * item)
 *   - Ordered lists (1. item)
 *   - Blockquotes (> text)
 *   - Headings (# text, ## text, ### text)
 *
 * All input is HTML-escaped before processing to prevent XSS.
 * The output is safe to use with v-html.
 */

/**
 * Escape HTML special characters.
 * @param {string} str
 * @returns {string}
 */
function escapeHtml(str) {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

/**
 * Render markdown-formatted text to HTML.
 *
 * @param {string} text — raw message text (may contain markdown)
 * @returns {string} — sanitized HTML
 */
export function renderMarkdown(text) {
  if (!text) return "";

  // First, extract code blocks to protect them from inline processing.
  const codeBlocks = [];
  let processed = text.replace(/```(\w*)\n?([\s\S]*?)```/g, (_, lang, code) => {
    const idx = codeBlocks.length;
    codeBlocks.push({ lang, code });
    return `\x00CODEBLOCK${idx}\x00`;
  });

  // Extract inline code to protect from further processing.
  const inlineCodes = [];
  processed = processed.replace(/`([^`\n]+)`/g, (_, code) => {
    const idx = inlineCodes.length;
    inlineCodes.push(code);
    return `\x00INLINE${idx}\x00`;
  });

  // Escape HTML in the remaining text.
  processed = escapeHtml(processed);

  // Process block-level elements line by line.
  const lines = processed.split("\n");
  const outputLines = [];
  let inList = false;
  let listType = null; // "ul" or "ol"

  for (let i = 0; i < lines.length; i++) {
    let line = lines[i];

    // Code block placeholder — restore later.
    if (line.match(/\x00CODEBLOCK\d+\x00/)) {
      if (inList) {
        outputLines.push(`</${listType}>`);
        inList = false;
        listType = null;
      }
      outputLines.push(line);
      continue;
    }

    // Blockquote.
    const bqMatch = line.match(/^&gt;\s?(.*)/);
    if (bqMatch) {
      if (inList) {
        outputLines.push(`</${listType}>`);
        inList = false;
        listType = null;
      }
      outputLines.push(`<blockquote class="border-l-2 border-accent-muted pl-3 text-text-secondary">${bqMatch[1]}</blockquote>`);
      continue;
    }

    // Headings.
    const headingMatch = line.match(/^(#{1,3})\s+(.*)/);
    if (headingMatch) {
      if (inList) {
        outputLines.push(`</${listType}>`);
        inList = false;
        listType = null;
      }
      const level = headingMatch[1].length;
      const sizes = { 1: "text-lg font-bold", 2: "text-base font-bold", 3: "text-sm font-semibold" };
      outputLines.push(`<div class="${sizes[level]} text-text-primary mt-2 mb-1">${headingMatch[2]}</div>`);
      continue;
    }

    // Unordered list item.
    const ulMatch = line.match(/^\s*[-*]\s+(.*)/);
    if (ulMatch) {
      if (!inList || listType !== "ul") {
        if (inList) outputLines.push(`</${listType}>`);
        outputLines.push('<ul class="list-disc pl-5 space-y-0.5">');
        inList = true;
        listType = "ul";
      }
      outputLines.push(`<li>${ulMatch[1]}</li>`);
      continue;
    }

    // Ordered list item.
    const olMatch = line.match(/^\s*\d+\.\s+(.*)/);
    if (olMatch) {
      if (!inList || listType !== "ol") {
        if (inList) outputLines.push(`</${listType}>`);
        outputLines.push('<ol class="list-decimal pl-5 space-y-0.5">');
        inList = true;
        listType = "ol";
      }
      outputLines.push(`<li>${olMatch[1]}</li>`);
      continue;
    }

    // Not a list item — close any open list.
    if (inList) {
      outputLines.push(`</${listType}>`);
      inList = false;
      listType = null;
    }

    outputLines.push(line);
  }

  // Close any trailing list.
  if (inList) {
    outputLines.push(`</${listType}>`);
  }

  processed = outputLines.join("\n");

  // Inline formatting (applied after block processing).
  // Bold: **text** or __text__
  processed = processed.replace(/\*\*(.+?)\*\*/g, '<strong class="font-bold">$1</strong>');
  processed = processed.replace(/__(.+?)__/g, '<strong class="font-bold">$1</strong>');

  // Italic: *text* or _text_ (but not inside words like foo_bar_baz)
  processed = processed.replace(/(?<!\w)\*([^*\n]+?)\*(?!\w)/g, '<em class="italic">$1</em>');
  processed = processed.replace(/(?<!\w)_([^_\n]+?)_(?!\w)/g, '<em class="italic">$1</em>');

  // Strikethrough: ~~text~~
  processed = processed.replace(/~~(.+?)~~/g, '<del class="line-through text-text-muted">$1</del>');

  // Markdown links: [text](url)
  processed = processed.replace(
    /\[([^\]]+)\]\(([^)]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer" class="text-info underline hover:text-accent">$1</a>',
  );

  // Auto-link URLs (but not already inside href="...")
  processed = processed.replace(
    /(?<!href="|">)(https?:\/\/[^\s<"]+)/g,
    '<a href="$1" target="_blank" rel="noopener noreferrer" class="text-info underline hover:text-accent">$1</a>',
  );

  // Restore inline code.
  processed = processed.replace(/\x00INLINE(\d+)\x00/g, (_, idx) => {
    const code = escapeHtml(inlineCodes[parseInt(idx, 10)]);
    return `<code class="rounded bg-bg-tertiary px-1.5 py-0.5 font-mono text-xs text-accent">${code}</code>`;
  });

  // Restore code blocks.
  processed = processed.replace(/\x00CODEBLOCK(\d+)\x00/g, (_, idx) => {
    const block = codeBlocks[parseInt(idx, 10)];
    const code = escapeHtml(block.code.trim());
    const langLabel = block.lang ? `<div class="mb-1 text-xs text-text-muted">${escapeHtml(block.lang)}</div>` : "";
    return `<div class="my-2 rounded-lg border border-border bg-bg-tertiary p-3 font-mono text-xs">${langLabel}<pre class="overflow-x-auto whitespace-pre-wrap"><code>${code}</code></pre></div>`;
  });

  // Convert remaining newlines to <br> (but not inside code blocks or lists).
  processed = processed.replace(/\n/g, "<br>");

  return processed;
}
