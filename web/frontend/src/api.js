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
