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
 *
 * Multi-channel support: messages are stored globally with a channel field.
 * Per-channel state (users, topic, unread count) is stored in channelState.
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

/** The user's own IRC nick (set on "connected" message). */
let ownNick = "";

/** @type {import('vue').ShallowRef<WebSocket|null>} */
const ws = shallowRef(null);
let reconnectAttempts = 0;
let reconnectTimer = null;
let intentionalClose = false;

export const chatStore = reactive({
  /** List of available channel names. */
  channels: [],
  /** Currently active/selected channel. */
  activeChannel: "",
  /** Per-channel state: { [channel]: { users: [], topic: "", unread: 0 } }. */
  channelState: {},
  /** Reactive array of chat messages (all channels). */
  messages: [],
  /** Reactive connection state (WS_STATE enum). */
  wsState: WS_STATE.DISCONNECTED,
  /** Total unread messages across all channels (for badge when not on chat page). */
  unreadCount: 0,
});

/**
 * Get or create per-channel state.
 * @param {string} channel
 * @returns {{ users: string[], topic: string, unread: number }}
 */
function getChannelState(channel) {
  if (!chatStore.channelState[channel]) {
    chatStore.channelState[channel] = { users: [], topic: "", unread: 0 };
  }
  return chatStore.channelState[channel];
}

/**
 * Switch the active channel.
 * @param {string} channel
 */
export function setActiveChannel(channel) {
  chatStore.activeChannel = channel;
  // Clear unread for this channel.
  const state = getChannelState(channel);
  state.unread = 0;
}

/**
 * Join a new channel via the WebSocket bridge.
 * @param {string} channel
 */
export function wsJoin(channel) {
  if (ws.value && ws.value.readyState === WebSocket.OPEN) {
    ws.value.send(JSON.stringify({ type: "join", channel }));
    if (!chatStore.channels.includes(channel)) {
      chatStore.channels = [...chatStore.channels, channel].sort();
    }
    getChannelState(channel);
  }
}

/**
 * Part (leave) a channel via the WebSocket bridge.
 * @param {string} channel
 */
export function wsPart(channel) {
  if (ws.value && ws.value.readyState === WebSocket.OPEN) {
    ws.value.send(JSON.stringify({ type: "part", channel }));
    chatStore.channels = chatStore.channels.filter((c) => c !== channel);
    delete chatStore.channelState[channel];
    // If we left the active channel, switch to the first available.
    if (chatStore.activeChannel === channel && chatStore.channels.length > 0) {
      setActiveChannel(chatStore.channels[0]);
    }
  }
}

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

  switch (msg.type) {
    case "connected":
      chatStore.wsState = WS_STATE.CONNECTED;
      ownNick = msg.nick || "";
      // Populate channel list from the server.
      if (msg.channels && msg.channels.length > 0) {
        chatStore.channels = [...msg.channels].sort();
        for (const ch of msg.channels) {
          getChannelState(ch);
        }
        // Set active channel to first if not already set.
        if (!chatStore.activeChannel || !chatStore.channels.includes(chatStore.activeChannel)) {
          chatStore.activeChannel = chatStore.channels[0];
        }
      }
      addSystemMessage(null, `Connected to IRC as ${msg.nick}`, now);
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
      // Track unread for non-active channels.
      if (msg.channel && msg.channel !== chatStore.activeChannel) {
        const state = getChannelState(msg.channel);
        state.unread++;
      }
      break;

    case "join": {
      const ch = msg.channel;
      if (ch) {
        // If we joined a new channel, add it to the channel list.
        if (msg.nick === ownNick && !chatStore.channels.includes(ch)) {
          chatStore.channels = [...chatStore.channels, ch].sort();
          getChannelState(ch);
        }
        addSystemMessage(ch, `${msg.nick} joined ${ch}`, now);
        const state = getChannelState(ch);
        if (!state.users.includes(msg.nick)) {
          state.users = [...state.users, msg.nick].sort();
        }
      }
      break;
    }

    case "part": {
      const ch = msg.channel;
      if (ch) {
        const reason = msg.text ? ` (${msg.text})` : "";
        addSystemMessage(ch, `${msg.nick} left ${ch}${reason}`, now);
        // If we left the channel, remove it from the list.
        if (msg.nick === ownNick) {
          chatStore.channels = chatStore.channels.filter((c) => c !== ch);
          delete chatStore.channelState[ch];
          if (chatStore.activeChannel === ch && chatStore.channels.length > 0) {
            chatStore.activeChannel = chatStore.channels[0];
          }
        } else {
          const state = getChannelState(ch);
          state.users = state.users.filter((u) => u !== msg.nick);
        }
      }
      break;
    }

    case "topic": {
      const ch = msg.channel;
      if (ch) {
        const state = getChannelState(ch);
        state.topic = msg.topic;
        addSystemMessage(ch, `${msg.nick} set topic: ${msg.topic}`, now);
      }
      break;
    }

    case "names": {
      const ch = msg.channel;
      if (ch && msg.users) {
        const state = getChannelState(ch);
        state.users = [...msg.users].sort();
      }
      break;
    }

    case "mode": {
      const ch = msg.channel;
      if (ch) {
        addSystemMessage(ch, `${msg.nick} set mode ${msg.mode} on ${ch}`, now);
      }
      break;
    }

    case "error":
      chatStore.wsState = WS_STATE.ERROR;
      addSystemMessage(null, `Error: ${msg.error}`, now);
      // Fatal errors (config/auth) should not trigger reconnect — the same
      // credentials will just fail again in a loop.
      if (msg.error && msg.error.includes("IRC connection failed")) {
        intentionalClose = true;
        clearReconnectTimer();
      }
      break;
  }
}

/** Append a message to the store, capping at MAX_MESSAGES to prevent memory leaks. */
function appendMessage(msg) {
  const updated = [...chatStore.messages, msg];
  chatStore.messages = updated.length > MAX_MESSAGES ? updated.slice(-MAX_MESSAGES) : updated;
}

/**
 * Add a system/event message to the message list.
 * @param {string|null} channel — the channel this event belongs to, or null for global.
 * @param {string} text
 * @param {number} time
 */
function addSystemMessage(channel, text, time) {
  appendMessage({
    id: `${time}-${++msgIdCounter}`,
    type: "system",
    channel: channel,
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
