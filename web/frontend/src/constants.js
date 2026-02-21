/**
 * Shared constants for the Murmur dashboard frontend.
 *
 * Design tokens (colors, fonts, spacing) live in src/styles/main.css
 * inside the Tailwind CSS 4 @theme block — that is the single source
 * of truth for all visual styling. Components consume them exclusively
 * via Tailwind utility classes (e.g. bg-bg-primary, text-accent).
 *
 * This file holds application-level constants: storage keys, API paths,
 * and any values shared across multiple modules.
 */

/** sessionStorage key for the logged-in nick (UX hint only — not a security boundary). */
export const SESSION_NICK_KEY = "murmur_nick";

/** sessionStorage key for the per-session HMAC signing key. */
export const SESSION_SIGNING_KEY = "murmur_signing_key";

/** Dashboard API endpoint paths. */
export const API = Object.freeze({
  LOGIN: "/dashboard/login",
  LOGOUT: "/dashboard/logout",
  STATUS: "/dashboard/status",
  WS: "/ws",
});
