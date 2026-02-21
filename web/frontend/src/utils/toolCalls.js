/**
 * Tool call detection and rendering utilities.
 *
 * Detects tool call patterns in bot messages and provides structured
 * data for rendering as collapsible cards with approve/deny buttons.
 */

/**
 * Detect if a message contains a tool call approval request.
 * Pattern: "⚠ Tool call requires approval: <name>(<args>). Reply !approve or !deny [id: <id>]"
 *
 * @param {string} text — message text
 * @returns {{ name: string, args: string, id: string } | null}
 */
export function detectApprovalRequest(text) {
  if (!text) return null;
  // Match the approval request pattern from agent.go line 1276.
  const match = text.match(
    /⚠\s*Tool call requires approval:\s*(\w+)\(([^)]*)\)\.\s*Reply !approve or !deny\s*\[id:\s*([a-f0-9]+)\]/,
  );
  if (!match) return null;
  return {
    name: match[1],
    args: match[2],
    id: match[3],
  };
}

/**
 * Detect if a message is a tool call result or status update.
 * Common patterns from the agent:
 *   - "approved: <tool>"
 *   - "denied: <tool>"
 *   - "approval timed out for <tool> — denied"
 *
 * @param {string} text
 * @returns {{ type: string, tool: string } | null}
 */
export function detectToolStatus(text) {
  if (!text) return null;

  const approved = text.match(/^approved:\s*(\w+)/);
  if (approved) return { type: "approved", tool: approved[1] };

  const denied = text.match(/^denied:\s*(\w+)/);
  if (denied) return { type: "denied", tool: denied[1] };

  const timeout = text.match(/approval timed out for (\w+)/);
  if (timeout) return { type: "timeout", tool: timeout[1] };

  return null;
}

/**
 * Check if a message looks like it contains a tool call result
 * (typically a longer structured response from the bot after executing a tool).
 *
 * @param {string} text
 * @returns {boolean}
 */
export function isToolResult(text) {
  // Tool results are typically multi-line or contain code blocks.
  if (!text) return false;
  return text.includes("```") || text.split("\n").length > 3;
}
