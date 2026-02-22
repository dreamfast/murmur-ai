// browser-server/server.js — Headless browser automation server for Murmur.
// Provides a simple HTTP API around Playwright for browser automation tasks.
// Single browser instance, pages keyed by session ID, auto-close after idle.

const http = require("http");
const dns = require("dns");
const { URL } = require("url");
const { chromium } = require("playwright");

const PORT = parseInt(process.env.PORT || "3001", 10);
const IDLE_TIMEOUT_MS = parseInt(process.env.IDLE_TIMEOUT_MS || "300000", 10); // 5 min
const MAX_CONTENT_LENGTH = parseInt(process.env.MAX_CONTENT_LENGTH || "50000", 10);
const MAX_REQUEST_BODY = parseInt(process.env.MAX_REQUEST_BODY || "1048576", 10); // 1MB
const MAX_SESSIONS = parseInt(process.env.MAX_SESSIONS || "10", 10);

// Blocked URL schemes for navigation guard.
const BLOCKED_SCHEMES = ["file:", "javascript:", "data:"];

// Private/reserved IP ranges for SSRF protection (mirrors Go isPrivateIP).
const PRIVATE_RANGES = [
  { prefix: "10.", v4: true },
  { prefix: "172.", v4: true, check: (ip) => { const b = parseInt(ip.split(".")[1], 10); return b >= 16 && b <= 31; } },
  { prefix: "192.168.", v4: true },
  { prefix: "127.", v4: true },
  { prefix: "169.254.", v4: true },
  { prefix: "0.", v4: true },
];

// isPrivateIP checks if an IP address is private/reserved.
function isPrivateIP(ip) {
  // IPv6 loopback.
  if (ip === "::1" || ip === "::") return true;
  // IPv4-mapped IPv6 (::ffff:x.x.x.x) — extract the IPv4 part.
  if (ip.startsWith("::ffff:")) ip = ip.substring(7);

  for (const range of PRIVATE_RANGES) {
    if (range.v4 && ip.startsWith(range.prefix)) {
      if (range.check) return range.check(ip);
      return true;
    }
  }
  return false;
}

// resolveAndValidate resolves a URL's hostname and checks that all IPs are
// public. This prevents DNS rebinding attacks where a hostname resolves to a
// private IP at navigation time (TOCTOU mitigation).
function resolveAndValidate(url) {
  return new Promise((resolve, reject) => {
    let parsed;
    try { parsed = new URL(url); } catch { return reject(new Error(`invalid URL: ${url}`)); }
    const hostname = parsed.hostname.replace(/^\[|\]$/g, ""); // strip IPv6 brackets

    // Literal IP — check directly.
    if (/^[\d.]+$/.test(hostname) || hostname.includes(":")) {
      if (isPrivateIP(hostname)) return reject(new Error("blocked: private/reserved IP"));
      return resolve();
    }

    // Resolve DNS and check all addresses.
    dns.resolve(hostname, (err4, addrs4) => {
      const v4 = err4 ? [] : addrs4;
      dns.resolve6(hostname, (err6, addrs6) => {
        const v6 = err6 ? [] : addrs6;
        const all = [...v4, ...v6];
        if (all.length === 0) return reject(new Error(`DNS resolution failed for ${hostname}`));
        for (const addr of all) {
          if (isPrivateIP(addr)) {
            return reject(new Error(`blocked: ${hostname} resolves to private IP ${addr}`));
          }
        }
        resolve();
      });
    });
  });
}

let browser = null;

// sessions maps session IDs to { page, timer }.
const sessions = new Map();

// launchBrowser ensures a single browser instance is running.
async function launchBrowser() {
  if (!browser || !browser.isConnected()) {
    browser = await chromium.launch({
      args: ["--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"],
    });
  }
  return browser;
}

// getOrCreateSession returns the page for a session, creating one if needed.
// Enforces a maximum session count to prevent resource exhaustion.
async function getOrCreateSession(sessionId) {
  if (sessions.has(sessionId)) {
    const session = sessions.get(sessionId);
    clearTimeout(session.timer);
    session.timer = setTimeout(() => closeSession(sessionId), IDLE_TIMEOUT_MS);
    return session.page;
  }

  if (sessions.size >= MAX_SESSIONS) {
    throw new Error(`session limit reached (max ${MAX_SESSIONS})`);
  }

  const b = await launchBrowser();
  const context = await b.newContext({
    userAgent:
      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
  });
  const page = await context.newPage();
  const timer = setTimeout(() => closeSession(sessionId), IDLE_TIMEOUT_MS);
  sessions.set(sessionId, { page, context, timer });
  return page;
}

// closeSession closes a session's page and context.
async function closeSession(sessionId) {
  const session = sessions.get(sessionId);
  if (!session) return;
  clearTimeout(session.timer);
  sessions.delete(sessionId);
  try {
    await session.page.close();
    await session.context.close();
  } catch (_) {
    // Ignore errors during cleanup.
  }
}

// isBlockedURL checks if a URL uses a blocked scheme.
function isBlockedURL(url) {
  const lower = url.toLowerCase().trim();
  return BLOCKED_SCHEMES.some((scheme) => lower.startsWith(scheme));
}

// readBody reads the full request body as a string, enforcing a size limit.
function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > MAX_REQUEST_BODY) {
        req.destroy();
        reject(new Error("request body too large"));
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString()));
    req.on("error", reject);
  });
}

// jsonResponse sends a JSON response.
function jsonResponse(res, status, data) {
  const body = JSON.stringify(data);
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(body);
}

// truncate truncates a string to maxLen characters.
function truncate(str, maxLen) {
  if (!str || str.length <= maxLen) return str || "";
  return str.substring(0, maxLen) + "... [truncated]";
}

// Route handlers.

async function handleNavigate(body) {
  const { session_id = "default", url } = body;
  if (!url) return { error: "url is required" };
  if (isBlockedURL(url)) return { error: `blocked URL scheme: ${url}` };

  // Resolve DNS and validate all IPs are public before navigation.
  // This mitigates DNS rebinding attacks (TOCTOU between Go validation
  // and Playwright navigation).
  await resolveAndValidate(url);

  const page = await getOrCreateSession(session_id);
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 });

  const title = await page.title();
  const text = await page.evaluate(() => document.body?.innerText || "");

  return {
    title,
    url: page.url(),
    content: truncate(text, MAX_CONTENT_LENGTH),
  };
}

async function handleScreenshot(body) {
  const { session_id = "default", full_page = false } = body;
  const page = await getOrCreateSession(session_id);

  const buffer = await page.screenshot({ fullPage: full_page, type: "png" });
  const title = await page.title();
  const viewport = page.viewportSize() || { width: 0, height: 0 };

  // Return metadata only — the base64 screenshot data is intentionally
  // excluded to avoid bloating the JSON response (screenshots can be several
  // MB). The Go tool only needs dimensions and title for its summary.
  return {
    title,
    width: viewport.width,
    height: viewport.height,
    size_bytes: buffer.length,
  };
}

async function handleClick(body) {
  const { session_id = "default", selector, text } = body;
  if (!selector && !text) return { error: "selector or text is required" };

  const page = await getOrCreateSession(session_id);

  if (text) {
    await page.getByText(text, { exact: false }).first().click({ timeout: 10000 });
  } else {
    await page.click(selector, { timeout: 10000 });
  }

  // Wait briefly for any navigation or DOM update.
  await page.waitForTimeout(500);

  return { success: true, url: page.url() };
}

async function handleType(body) {
  const { session_id = "default", selector, text } = body;
  if (!selector) return { error: "selector is required" };
  if (text === undefined) return { error: "text is required" };

  const page = await getOrCreateSession(session_id);
  await page.fill(selector, text, { timeout: 10000 });

  return { success: true };
}

async function handleEvaluate(body) {
  const { session_id = "default", script } = body;
  if (!script) return { error: "script is required" };

  const page = await getOrCreateSession(session_id);
  const result = await page.evaluate(script);

  return { result: truncate(String(result), MAX_CONTENT_LENGTH) };
}

async function handleContent(body) {
  const { session_id = "default" } = body;
  const page = await getOrCreateSession(session_id);

  const title = await page.title();
  // Extract readable text content, stripping scripts and styles.
  const text = await page.evaluate(() => {
    // Remove script and style elements.
    const clone = document.cloneNode(true);
    clone.querySelectorAll("script, style, noscript").forEach((el) => el.remove());
    return clone.body?.innerText || "";
  });

  return {
    title,
    url: page.url(),
    content: truncate(text, MAX_CONTENT_LENGTH),
  };
}

async function handleScroll(body) {
  const { session_id = "default", direction = "down", amount = 500 } = body;
  const page = await getOrCreateSession(session_id);

  const delta = direction === "up" ? -Math.abs(amount) : Math.abs(amount);
  await page.evaluate((d) => window.scrollBy(0, d), delta);

  // Wait briefly for lazy-loaded content.
  await page.waitForTimeout(300);

  const scrollY = await page.evaluate(() => window.scrollY);
  const scrollHeight = await page.evaluate(() => document.body.scrollHeight);

  return { scroll_y: scrollY, scroll_height: scrollHeight };
}

// HTTP server.
const server = http.createServer(async (req, res) => {
  // Health check.
  if (req.method === "GET" && req.url === "/health") {
    return jsonResponse(res, 200, { status: "ok", sessions: sessions.size });
  }

  if (req.method !== "POST") {
    return jsonResponse(res, 405, { error: "method not allowed" });
  }

  let body;
  try {
    const raw = await readBody(req);
    body = raw ? JSON.parse(raw) : {};
  } catch (e) {
    if (e.message === "request body too large") {
      return jsonResponse(res, 413, { error: "request body too large" });
    }
    return jsonResponse(res, 400, { error: "invalid JSON body" });
  }

  const routes = {
    "/navigate": handleNavigate,
    "/screenshot": handleScreenshot,
    "/click": handleClick,
    "/type": handleType,
    "/evaluate": handleEvaluate,
    "/content": handleContent,
    "/scroll": handleScroll,
  };

  const handler = routes[req.url];
  if (!handler) {
    return jsonResponse(res, 404, { error: "not found" });
  }

  try {
    const result = await handler(body);
    if (result.error) {
      return jsonResponse(res, 400, result);
    }
    return jsonResponse(res, 200, result);
  } catch (e) {
    console.error(`Error handling ${req.url}:`, e.message);
    return jsonResponse(res, 500, { error: e.message });
  }
});

// Graceful shutdown.
process.on("SIGTERM", async () => {
  console.log("Shutting down...");
  server.close();
  for (const [id] of sessions) {
    await closeSession(id);
  }
  if (browser) await browser.close();
  process.exit(0);
});

server.listen(PORT, () => {
  console.log(`Browser server listening on port ${PORT}`);
});
