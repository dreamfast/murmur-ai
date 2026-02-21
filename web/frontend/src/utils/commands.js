/**
 * IRC command definitions for autocomplete.
 *
 * Each command has a name (with ! prefix), a short description,
 * and optional argument hints.
 */
export const IRC_COMMANDS = [
  { name: "!help", desc: "Show available commands", args: "" },
  { name: "!status", desc: "Show server status", args: "" },
  { name: "!clients", desc: "List connected clients", args: "" },
  { name: "!tools", desc: "List available tools", args: "" },
  { name: "!model", desc: "Switch LLM model", args: "[provider]" },
  { name: "!history", desc: "Show conversation history", args: "" },
  { name: "!forget", desc: "Clear conversation history", args: "" },
  { name: "!flush", desc: "Flush pending approvals", args: "" },
  { name: "!notes", desc: "List saved notes", args: "[search]" },
  { name: "!approve", desc: "Approve a pending action", args: "[id]" },
  { name: "!deny", desc: "Deny a pending action", args: "[id]" },
  { name: "!pending", desc: "Show pending approvals", args: "" },
  { name: "!tasks", desc: "List scheduled tasks", args: "" },
  { name: "!task", desc: "Manage scheduled tasks", args: "<add|remove|list>" },
  { name: "!user", desc: "Manage user permissions", args: "<nick> <action>" },
  { name: "!channel", desc: "Manage channel permissions", args: "<channel> <action>" },
  { name: "!debug", desc: "Toggle debug settings", args: "[setting]" },
  { name: "!reload", desc: "Reload server configuration", args: "" },
];

/**
 * Filter commands matching the given prefix.
 *
 * @param {string} prefix — text to match (e.g. "!st")
 * @returns {Array<{name: string, desc: string, args: string}>}
 */
export function matchCommands(prefix) {
  if (!prefix || !prefix.startsWith("!")) return [];
  const lower = prefix.toLowerCase();
  return IRC_COMMANDS.filter((cmd) => cmd.name.startsWith(lower));
}
