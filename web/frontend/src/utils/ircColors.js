/**
 * IRC color code parser.
 *
 * Converts mIRC formatting codes to HTML spans with inline styles.
 * Supports:
 *   \x02        — bold toggle
 *   \x1D        — italic toggle
 *   \x1F        — underline toggle
 *   \x16        — reverse (swap fg/bg)
 *   \x0F        — reset all formatting
 *   \x03FG      — foreground color (0-98)
 *   \x03FG,BG   — foreground + background color
 *   \x03        — reset color (no digits)
 *
 * Color palette follows the extended mIRC 16-color + 83 extended palette.
 */

/** Standard mIRC 16-color palette. */
const IRC_COLORS_16 = [
  "#ffffff", // 0  white
  "#000000", // 1  black
  "#00007f", // 2  blue (navy)
  "#009300", // 3  green
  "#ff0000", // 4  red
  "#7f0000", // 5  brown (maroon)
  "#9c009c", // 6  purple
  "#fc7f00", // 7  orange (olive)
  "#ffff00", // 8  yellow
  "#00fc00", // 9  light green (lime)
  "#009393", // 10 teal (cyan)
  "#00ffff", // 11 light cyan (aqua)
  "#0000fc", // 12 light blue (royal)
  "#ff00ff", // 13 pink (fuchsia)
  "#7f7f7f", // 14 grey
  "#d2d2d2", // 15 light grey (silver)
];

/**
 * Extended mIRC color palette (indices 16-98).
 * These follow the 6x6x6 color cube + greyscale ramp convention
 * used by most modern IRC clients.
 */
function getExtendedColor(index) {
  if (index < 16) return IRC_COLORS_16[index];
  if (index > 98) return null;

  // 16-87: 6x6x6 color cube
  if (index <= 87) {
    const i = index - 16;
    const r = Math.floor(i / 36);
    const g = Math.floor((i % 36) / 6);
    const b = i % 6;
    const toHex = (v) => {
      const val = v === 0 ? 0 : 55 + v * 40;
      return val.toString(16).padStart(2, "0");
    };
    return `#${toHex(r)}${toHex(g)}${toHex(b)}`;
  }

  // 88-98: greyscale ramp
  const grey = Math.round(((index - 88) / 10) * 230 + 25);
  const hex = grey.toString(16).padStart(2, "0");
  return `#${hex}${hex}${hex}`;
}

/**
 * Escape HTML special characters to prevent XSS.
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
 * Parse IRC formatting codes in a string and return sanitized HTML.
 *
 * @param {string} text — raw IRC message text (may contain control chars)
 * @returns {string} — HTML string with <span> elements for formatting
 */
export function parseIRCColors(text) {
  if (!text) return "";

  let result = "";
  let bold = false;
  let italic = false;
  let underline = false;
  let fg = null;
  let bg = null;
  let spanOpen = false;

  /** Build a style string from current formatting state. */
  function buildStyle() {
    const parts = [];
    if (bold) parts.push("font-weight:bold");
    if (italic) parts.push("font-style:italic");
    if (underline) parts.push("text-decoration:underline");
    if (fg !== null) {
      const color = getExtendedColor(fg);
      if (color) parts.push(`color:${color}`);
    }
    if (bg !== null) {
      const color = getExtendedColor(bg);
      if (color) parts.push(`background-color:${color}`);
    }
    return parts.join(";");
  }

  /** Close current span if open, open new one if needed. */
  function updateSpan() {
    if (spanOpen) {
      result += "</span>";
      spanOpen = false;
    }
    const style = buildStyle();
    if (style) {
      result += `<span style="${style}">`;
      spanOpen = true;
    }
  }

  let i = 0;
  while (i < text.length) {
    const ch = text.charCodeAt(i);

    switch (ch) {
      case 0x02: // Bold toggle
        bold = !bold;
        updateSpan();
        i++;
        break;

      case 0x1d: // Italic toggle
        italic = !italic;
        updateSpan();
        i++;
        break;

      case 0x1f: // Underline toggle
        underline = !underline;
        updateSpan();
        i++;
        break;

      case 0x16: // Reverse (swap fg/bg)
        [fg, bg] = [bg, fg];
        updateSpan();
        i++;
        break;

      case 0x0f: // Reset all
        bold = false;
        italic = false;
        underline = false;
        fg = null;
        bg = null;
        updateSpan();
        i++;
        break;

      case 0x03: { // Color
        i++;
        // Parse foreground color (1-2 digits).
        let fgStr = "";
        if (i < text.length && text[i] >= "0" && text[i] <= "9") {
          fgStr += text[i++];
          if (i < text.length && text[i] >= "0" && text[i] <= "9") {
            fgStr += text[i++];
          }
        }

        if (fgStr === "") {
          // Bare \x03 — reset colors.
          fg = null;
          bg = null;
        } else {
          fg = parseInt(fgStr, 10);
          // Parse optional background color.
          if (i < text.length && text[i] === ",") {
            i++; // skip comma
            let bgStr = "";
            if (i < text.length && text[i] >= "0" && text[i] <= "9") {
              bgStr += text[i++];
              if (i < text.length && text[i] >= "0" && text[i] <= "9") {
                bgStr += text[i++];
              }
            }
            if (bgStr !== "") {
              bg = parseInt(bgStr, 10);
            }
          }
        }
        updateSpan();
        break;
      }

      default:
        // Regular character — escape and append.
        result += escapeHtml(text[i]);
        i++;
        break;
    }
  }

  // Close any remaining open span.
  if (spanOpen) {
    result += "</span>";
  }

  return result;
}

/**
 * Strip all IRC formatting codes from a string, returning plain text.
 *
 * @param {string} text — raw IRC message text
 * @returns {string} — plain text without formatting codes
 */
export function stripIRCColors(text) {
  if (!text) return "";
  // Remove color codes: \x03 followed by optional digits and comma+digits.
  // Remove formatting toggles: \x02, \x1D, \x1F, \x16, \x0F.
  return text
    .replace(/\x03(\d{1,2}(,\d{1,2})?)?/g, "")
    .replace(/[\x02\x1d\x1f\x16\x0f]/g, "");
}
