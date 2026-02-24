/**
 * Dashboard API client with request signing.
 *
 * Every non-login request includes:
 *   X-Request-Timestamp — Unix epoch seconds (server rejects if >30s stale)
 *   X-Request-Signature — HMAC-SHA256(signingKey, timestamp + method + path + body)
 *
 * WebSocket connections use query parameters (t=timestamp, s=signature)
 * because the browser WebSocket API does not support custom HTTP headers.
 *
 * The signing key is returned by the login endpoint and stored in
 * sessionStorage. Login itself is unsigned (no key yet).
 */

import { SESSION_SIGNING_KEY } from "./constants.js";

/**
 * Compute HMAC-SHA256 of the given message using the Web Crypto API.
 * Returns a hex-encoded string.
 */
async function hmacSHA256(keyHex, message) {
  const keyBytes = hexToBytes(keyHex);
  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    keyBytes,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const msgBytes = new TextEncoder().encode(message);
  const sig = await crypto.subtle.sign("HMAC", cryptoKey, msgBytes);
  return bytesToHex(new Uint8Array(sig));
}

/** Convert a hex string to a Uint8Array. */
function hexToBytes(hex) {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    bytes[i / 2] = parseInt(hex.substring(i, i + 2), 16);
  }
  return bytes;
}

/** Convert a Uint8Array to a hex string. */
function bytesToHex(bytes) {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

/**
 * Send a signed fetch request to a dashboard API endpoint.
 *
 * @param {string} path   — API path (e.g. "/dashboard/logout")
 * @param {object} opts   — fetch options (method, body, headers, etc.)
 * @returns {Promise<Response>}
 */
export async function signedFetch(path, opts = {}) {
  const method = (opts.method || "GET").toUpperCase();
  const body = opts.body || "";
  const timestamp = Math.floor(Date.now() / 1000).toString();

  const headers = new Headers(opts.headers || {});
  headers.set("X-Request-Timestamp", timestamp);

  const signingKey = sessionStorage.getItem(SESSION_SIGNING_KEY);
  if (signingKey) {
    const payload = timestamp + method + path + body;
    const signature = await hmacSHA256(signingKey, payload);
    headers.set("X-Request-Signature", signature);
  }

  return fetch(path, { ...opts, headers });
}

/**
 * Build a signed WebSocket URL. The browser WebSocket API cannot set
 * custom HTTP headers, so the timestamp and signature are passed as
 * query parameters (t and s).
 *
 * @param {string} path — WebSocket path (e.g. "/ws")
 * @returns {Promise<string>} — full WebSocket URL with signed query params
 */
export async function signedWebSocketURL(path) {
  const timestamp = Math.floor(Date.now() / 1000).toString();
  const signingKey = sessionStorage.getItem(SESSION_SIGNING_KEY);

  let url = `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${path}`;

  if (signingKey) {
    const payload = timestamp + "GET" + path;
    const signature = await hmacSHA256(signingKey, payload);
    url += `?t=${timestamp}&s=${signature}`;
  }

  return url;
}

// --- Admin API client ---

const ADMIN_API = "/dashboard/api";

/**
 * Send a signed JSON request to an admin API endpoint.
 * Handles JSON serialization and Content-Type header automatically.
 *
 * @param {string} path   — API sub-path (e.g. "/users")
 * @param {object} opts   — { method, body (object), ... }
 * @returns {Promise<{ok: boolean, data?: any, error?: string, status: number}>}
 */
async function adminFetch(path, opts = {}) {
  const fullPath = ADMIN_API + path;
  const fetchOpts = { method: opts.method || "GET" };

  if (opts.body !== undefined) {
    fetchOpts.body = JSON.stringify(opts.body);
    fetchOpts.headers = { "Content-Type": "application/json" };
  }

  const res = await signedFetch(fullPath, fetchOpts);
  let json;
  try {
    json = await res.json();
  } catch {
    return { ok: false, error: `Server returned ${res.status}`, status: res.status };
  }
  return { ...json, status: res.status };
}

// --- Users ---

/** List all users. */
export function adminListUsers() {
  return adminFetch("/users");
}

/** Get a single user by nick. */
export function adminGetUser(nick) {
  return adminFetch(`/users/${encodeURIComponent(nick)}`);
}

/** Create a new user. */
export function adminCreateUser(user) {
  return adminFetch("/users", { method: "POST", body: user });
}

/** Update an existing user (partial update). */
export function adminUpdateUser(nick, fields) {
  return adminFetch(`/users/${encodeURIComponent(nick)}`, {
    method: "PUT",
    body: fields,
  });
}

/** Delete a user by nick. */
export function adminDeleteUser(nick) {
  return adminFetch(`/users/${encodeURIComponent(nick)}`, {
    method: "DELETE",
  });
}

// --- Tools ---

/** List all tools (server + client + custom) with source. */
export function adminListTools() {
  return adminFetch("/tools");
}

/** List custom tools only. */
export function adminListCustomTools() {
  return adminFetch("/tools/custom");
}

/** Create a new custom tool. */
export function adminCreateCustomTool(tool) {
  return adminFetch("/tools/custom", { method: "POST", body: tool });
}

/** Update an existing custom tool (partial update). */
export function adminUpdateCustomTool(name, fields) {
  return adminFetch(`/tools/custom/${encodeURIComponent(name)}`, {
    method: "PUT",
    body: fields,
  });
}

/** Delete a custom tool by name. */
export function adminDeleteCustomTool(name) {
  return adminFetch(`/tools/custom/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
}

/** Toggle a custom tool enabled/disabled. */
export function adminToggleCustomTool(name, enabled) {
  return adminFetch(`/tools/custom/${encodeURIComponent(name)}/toggle`, {
    method: "POST",
    body: { enabled },
  });
}

// --- Tasks ---

/** List all scheduled tasks. */
export function adminListTasks() {
  return adminFetch("/tasks");
}

/** Create a new task (cron or one-shot). */
export function adminCreateTask(task) {
  return adminFetch("/tasks", { method: "POST", body: task });
}

/** Delete a task by ID. */
export function adminDeleteTask(id) {
  return adminFetch(`/tasks/${id}`, { method: "DELETE" });
}

/** Toggle a task enabled/disabled. */
export function adminToggleTask(id, enabled) {
  return adminFetch(`/tasks/${id}/toggle`, {
    method: "POST",
    body: { enabled },
  });
}

// --- Channels ---

/** List all channel settings. */
export function adminListChannels() {
  return adminFetch("/channels");
}

/** Update channel settings. Channel name must include the # prefix. */
export function adminUpdateChannel(channel, settings) {
  return adminFetch(`/channels/${encodeURIComponent(channel)}`, {
    method: "PUT",
    body: settings,
  });
}

// --- Providers ---

/** List all configured LLM providers. */
export function adminListProviders() {
  return adminFetch("/providers");
}

// --- Statistics ---

/**
 * Build a query string from a params object, omitting empty values.
 * @param {object} params — key-value pairs
 * @returns {string} — query string (with leading ?) or empty string
 */
function buildQuery(params) {
  const entries = Object.entries(params || {}).filter(
    ([, v]) => v !== undefined && v !== null && v !== "",
  );
  if (entries.length === 0) return "";
  return "?" + new URLSearchParams(entries).toString();
}

/** Get a high-level summary of usage statistics. */
export function adminGetStatsSummary(params) {
  return adminFetch("/stats/summary" + buildQuery(params));
}

/** Get time-bucketed aggregation of usage statistics. */
export function adminGetStatsAggregate(params) {
  return adminFetch("/stats/aggregate" + buildQuery(params));
}

/** Get aggregated tool usage statistics. */
export function adminGetStatsTools(params) {
  return adminFetch("/stats/tools" + buildQuery(params));
}

/** Get aggregated per-provider statistics. */
export function adminGetStatsProviders(params) {
  return adminFetch("/stats/providers" + buildQuery(params));
}

/** Get a paginated list of raw usage statistics. */
export function adminGetStatsList(params) {
  return adminFetch("/stats" + buildQuery(params));
}

// --- System ---

/** Trigger a server configuration reload. */
export function adminReloadConfig() {
  return adminFetch("/system/reload", { method: "POST", body: {} });
}
