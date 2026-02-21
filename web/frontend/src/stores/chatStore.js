/**
 * Shared reactive state for the chat view.
 *
 * This module provides a singleton reactive store that is shared between
 * the Chat layout (sidebar, user list, WebSocket lifecycle) and ChatContent
 * (messages, input). Using a plain reactive object avoids the need for
 * Pinia or Vuex.
 *
 * The WebSocket connection is managed here so it persists across child
 * route changes (Overview, Chat, Admin). This enables unread message
 * tracking when the user is not on the chat page.
 */

import { reactive, shallowRef } from "vue";
import { signedWebSocketURL } from "../api.js";
import { API } from "../constants.js";

/** Connection states exposed to components. */
export const WS_STATE = Object.freeze({
  CONNECTING: "connecting",
  CONNECTED: "connected",
  DISCONNECTED: "disconnected",
  ERROR: "error",
});

/** Maximum reconnect delay in milliseconds. */
const MAX_RECONNECT_DELAY = 30_000;
/** Base reconnect delay in milliseconds. */
const BASE_RECONNECT_DELAY = 1_000;
/** Maximum number of reconnect attempts before giving up. */
const MAX_RECONNECT_ATTEMPTS = 10;
/** Maximum number of messages to keep in the store to prevent memory leaks. */
const MAX_MESSAGES = 1000;

/** Monotonic counter for generating unique message IDs. */
let msgIdCounter = 0;

/** @type {import('vue').ShallowRef<WebSocket|null>} */
const ws = shallowRef(null);
let reconnectAttempts = 0;
let reconnectTimer = null;
let intentionalClose = false;

export const chatStore = reactive({
  /** Sorted list of nicks in the current channel. */
  users: [],
  /** Current channel topic. */
  topic: "",
  /** Current channel name. */
  channel: "#murmur",
  /** Reactive array of chat messages. */
  messages: [],
  /** Reactive connection state (WS_STATE enum). */
  wsState: WS_STATE.DISCONNECTED,
  /** Number of unread messages since the user last viewed the chat. */
  unreadCount: 0,
});

/**
 * Connect to the WebSocket bridge.
 *
 * Called by Chat.vue (the layout) on mount. The connection persists
 * across child route changes so messages are received even when the
 * user is on the Overview or Admin page.
 */
export async function wsConnect() {
  intentionalClose = false;
  chatStore.wsState = WS_STATE.CONNECTING;

  try {
    const url = await signedWebSocketURL(API.WS);
    const socket = new WebSocket(url);
    ws.value = socket;

    socket.onopen = () => {
      reconnectAttempts = 0;
      // State will be set to CONNECTED when we receive the "connected"
      // message from the bridge (after IRC handshake completes).
    };

    socket.onmessage = (event) => {
      let msg;
      try {
        msg = JSON.parse(event.data);
      } catch {
        return;
      }
      handleMessage(msg);
    };

    socket.onclose = () => {
      ws.value = null;
      if (!intentionalClose) {
        chatStore.wsState = WS_STATE.DISCONNECTED;
        scheduleReconnect();
      }
    };

    socket.onerror = () => {
      chatStore.wsState = WS_STATE.ERROR;
    };
  } catch {
    chatStore.wsState = WS_STATE.ERROR;
    scheduleReconnect();
  }
}

/** Disconnect intentionally (no reconnect). Called by Chat.vue on unmount. */
export function wsDisconnect() {
  intentionalClose = true;
  clearReconnectTimer();
  if (ws.value) {
    ws.value.close();
    ws.value = null;
  }
  chatStore.wsState = WS_STATE.DISCONNECTED;
}

/**
 * Send a message to the IRC channel via the WebSocket bridge.
 * @param {string} channel
 * @param {string} text
 */
export function wsSend(channel, text) {
  if (ws.value && ws.value.readyState === WebSocket.OPEN) {
    ws.value.send(JSON.stringify({ type: "message", channel, text }));
  }
}

/** Reset the unread message counter (called when navigating to chat). */
export function clearUnread() {
  chatStore.unreadCount = 0;
}

/** Handle an incoming WebSocket message from the bridge. */
function handleMessage(msg) {
  const now = Date.now();
  const channel = chatStore.channel;

  switch (msg.type) {
    case "connected":
      chatStore.wsState = WS_STATE.CONNECTED;
      addSystemMessage(`Connected to IRC as ${msg.nick}`, now);
      break;

    case "message":
      appendMessage({
        id: `${now}-${++msgIdCounter}`,
        type: "message",
        nick: msg.nick,
        text: msg.text,
        channel: msg.channel,
        time: now,
      });
      break;

    case "join":
      if (msg.channel === channel) {
        addSystemMessage(`${msg.nick} joined ${msg.channel}`, now);
        if (!chatStore.users.includes(msg.nick)) {
          chatStore.users = [...chatStore.users, msg.nick].sort();
        }
      }
      break;

    case "part":
      if (msg.channel === channel) {
        const reason = msg.text ? ` (${msg.text})` : "";
        addSystemMessage(`${msg.nick} left ${msg.channel}${reason}`, now);
        chatStore.users = chatStore.users.filter((u) => u !== msg.nick);
      }
      break;

    case "topic":
      if (msg.channel === channel) {
        chatStore.topic = msg.topic;
        addSystemMessage(`${msg.nick} set topic: ${msg.topic}`, now);
      }
      break;

    case "names":
      if (msg.channel === channel && msg.users) {
        chatStore.users = [...msg.users].sort();
      }
      break;

    case "mode":
      if (msg.channel === channel) {
        addSystemMessage(
          `${msg.nick} set mode ${msg.mode} on ${msg.channel}`,
          now,
        );
      }
      break;

    case "error":
      chatStore.wsState = WS_STATE.ERROR;
      addSystemMessage(`Error: ${msg.error}`, now);
      break;
  }
}

/** Append a message to the store, capping at MAX_MESSAGES to prevent memory leaks. */
function appendMessage(msg) {
  const updated = [...chatStore.messages, msg];
  chatStore.messages = updated.length > MAX_MESSAGES ? updated.slice(-MAX_MESSAGES) : updated;
}

/** Add a system/event message to the message list. */
function addSystemMessage(text, time) {
  appendMessage({
    id: `${time}-${++msgIdCounter}`,
    type: "system",
    text,
    time,
  });
}

/** Schedule a reconnect with exponential backoff. */
function scheduleReconnect() {
  if (intentionalClose || reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
    return;
  }
  const delay = Math.min(
    BASE_RECONNECT_DELAY * Math.pow(2, reconnectAttempts),
    MAX_RECONNECT_DELAY,
  );
  reconnectAttempts++;
  reconnectTimer = setTimeout(() => wsConnect(), delay);
}

/** Clear any pending reconnect timer. */
function clearReconnectTimer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}
