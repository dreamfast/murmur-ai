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
 * Per-channel state (users, topic, unread count, userModes) is stored in
 * channelState.
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

/**
 * Get the user's own IRC nick.
 * @returns {string}
 */
export function getOwnNick() {
  return ownNick;
}

/** @type {import('vue').ShallowRef<WebSocket|null>} */
const ws = shallowRef(null);
let reconnectAttempts = 0;
let reconnectTimer = null;
let intentionalClose = false;

/**
 * Case-insensitive nick comparison (IRC nicks are case-insensitive).
 * @param {string} a
 * @param {string} b
 * @returns {boolean}
 */
function nickEq(a, b) {
  return a.toLowerCase() === b.toLowerCase();
}

export const chatStore = reactive({
  /** List of available channel names. */
  channels: [],
  /** Currently active/selected channel. */
  activeChannel: "",
  /** Per-channel state: { [channel]: { users: [], topic: "", unread: 0, userModes: {} } }. */
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
 * @returns {{ users: string[], topic: string, unread: number, userModes: Object<string, string> }}
 */
function getChannelState(channel) {
  if (!chatStore.channelState[channel]) {
    chatStore.channelState[channel] = { users: [], topic: "", unread: 0, userModes: {} };
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
  persistChannels();
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
    persistChannels();
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
    persistChannels();
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

// ---------------------------------------------------------------------------
// Channel persistence (sessionStorage)
// ---------------------------------------------------------------------------

/** SessionStorage key for persisted channel list. */
const STORAGE_CHANNELS_KEY = "murmur_channels";
/** SessionStorage key for persisted active channel. */
const STORAGE_ACTIVE_KEY = "murmur_active_channel";

/** Persist the current channel list and active channel to sessionStorage. */
function persistChannels() {
  try {
    sessionStorage.setItem(STORAGE_CHANNELS_KEY, JSON.stringify(chatStore.channels));
    if (chatStore.activeChannel) {
      sessionStorage.setItem(STORAGE_ACTIVE_KEY, chatStore.activeChannel);
    }
  } catch {
    // sessionStorage may be unavailable (private browsing, quota exceeded).
  }
}

/**
 * Load persisted channels from sessionStorage.
 * @returns {{ channels: string[], activeChannel: string }}
 */
function loadPersistedChannels() {
  try {
    const raw = sessionStorage.getItem(STORAGE_CHANNELS_KEY);
    const channels = raw ? JSON.parse(raw) : [];
    const activeChannel = sessionStorage.getItem(STORAGE_ACTIVE_KEY) || "";
    return { channels: Array.isArray(channels) ? channels : [], activeChannel };
  } catch {
    return { channels: [], activeChannel: "" };
  }
}

// ---------------------------------------------------------------------------
// Local echo dedup
// ---------------------------------------------------------------------------

/** Recent local echoes for dedup. Array of {text, channel, time}. Max 10 entries. */
const localEchoes = [];
/** Time window for local echo dedup (milliseconds). */
const LOCAL_ECHO_WINDOW = 10_000;

/**
 * Record a local echo for dedup. Called from ChatContent when sending.
 * @param {string} channel
 * @param {string} text
 */
export function trackLocalEcho(channel, text) {
  localEchoes.push({ channel, text, time: Date.now() });
  if (localEchoes.length > 10) localEchoes.shift();
}

/**
 * Check if an incoming message matches a local echo (duplicate).
 * Returns true if it's a duplicate and removes the matched echo.
 * @param {string} channel
 * @param {string} nick
 * @param {string} text
 * @param {number} timestamp — message timestamp from server
 * @returns {boolean}
 */
function isLocalEchoDuplicate(channel, nick, text, timestamp) {
  if (!nickEq(nick, ownNick)) return false;
  // Skip dedup for historical messages (timestamp > 30s in the past).
  const now = Date.now();
  if (timestamp && (now - timestamp) > 30_000) return false;

  const idx = localEchoes.findIndex(
    (e) => e.channel === channel && e.text === text && (now - e.time) < LOCAL_ECHO_WINDOW,
  );
  if (idx !== -1) {
    localEchoes.splice(idx, 1);
    return true;
  }
  return false;
}

// ---------------------------------------------------------------------------
// Message handling
// ---------------------------------------------------------------------------

/** Handle an incoming WebSocket message from the bridge. */
function handleMessage(msg) {
  const now = Date.now();

  switch (msg.type) {
    case "connected": {
      chatStore.wsState = WS_STATE.CONNECTED;
      ownNick = msg.nick || "";
      // Populate channel list from the server.
      const serverChannels = msg.channels && msg.channels.length > 0 ? [...msg.channels] : [];
      for (const ch of serverChannels) {
        getChannelState(ch);
      }
      // Merge persisted channels — re-join any that the server didn't auto-join.
      const saved = loadPersistedChannels();
      const allChannels = new Set([...serverChannels, ...saved.channels]);
      for (const ch of saved.channels) {
        if (!serverChannels.includes(ch) && ws.value && ws.value.readyState === WebSocket.OPEN) {
          ws.value.send(JSON.stringify({ type: "join", channel: ch }));
          getChannelState(ch);
        }
      }
      chatStore.channels = [...allChannels].sort();
      // Restore active channel from session, or fall back to first channel.
      if (saved.activeChannel && allChannels.has(saved.activeChannel)) {
        chatStore.activeChannel = saved.activeChannel;
      } else if (!chatStore.activeChannel || !allChannels.has(chatStore.activeChannel)) {
        chatStore.activeChannel = chatStore.channels[0] || "";
      }
      persistChannels();
      addSystemMessage(null, `Connected to IRC as ${msg.nick}`, now);
      break;
    }

    case "message": {
      const msgTime = msg.timestamp || now;
      // Skip if this is a server echo of our own local message.
      if (isLocalEchoDuplicate(msg.channel, msg.nick, msg.text, msgTime)) break;
      appendMessage({
        id: `${msgTime}-${++msgIdCounter}`,
        type: "message",
        nick: msg.nick,
        text: msg.text,
        channel: msg.channel,
        time: msgTime,
      });
      // Track unread for non-active channels.
      if (msg.channel && msg.channel !== chatStore.activeChannel) {
        const state = getChannelState(msg.channel);
        state.unread++;
      }
      break;
    }

    case "join": {
      const ch = msg.channel;
      if (ch) {
        // If we joined a new channel, add it to the channel list.
        if (nickEq(msg.nick, ownNick) && !chatStore.channels.includes(ch)) {
          chatStore.channels = [...chatStore.channels, ch].sort();
          getChannelState(ch);
          persistChannels();
        }
        addSystemMessage(ch, `${msg.nick} joined ${ch}`, now);
        const state = getChannelState(ch);
        if (!state.users.some((u) => nickEq(u, msg.nick))) {
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
        if (nickEq(msg.nick, ownNick)) {
          chatStore.channels = chatStore.channels.filter((c) => c !== ch);
          delete chatStore.channelState[ch];
          if (chatStore.activeChannel === ch && chatStore.channels.length > 0) {
            chatStore.activeChannel = chatStore.channels[0];
          }
          persistChannels();
        } else {
          const state = getChannelState(ch);
          state.users = state.users.filter((u) => !nickEq(u, msg.nick));
          if (state.userModes) delete state.userModes[msg.nick];
        }
      }
      break;
    }

    case "quit": {
      const reason = msg.text ? ` (${msg.text})` : "";
      for (const [ch, state] of Object.entries(chatStore.channelState)) {
        if (state.users.some((u) => nickEq(u, msg.nick))) {
          state.users = state.users.filter((u) => !nickEq(u, msg.nick));
          if (state.userModes) delete state.userModes[msg.nick];
          addSystemMessage(ch, `${msg.nick} has quit${reason}`, now);
        }
      }
      break;
    }

    case "kick": {
      const ch = msg.channel;
      if (!ch) break;
      const kicker = msg.mode || "";
      const reason = msg.text ? ` (${msg.text})` : "";
      addSystemMessage(ch, `${msg.nick} was kicked by ${kicker}${reason}`, now);
      if (nickEq(msg.nick, ownNick)) {
        // Self-kicked — remove channel (same as part logic).
        chatStore.channels = chatStore.channels.filter((c) => c !== ch);
        delete chatStore.channelState[ch];
        if (chatStore.activeChannel === ch && chatStore.channels.length > 0) {
          chatStore.activeChannel = chatStore.channels[0];
        }
        persistChannels();
      } else {
        const state = getChannelState(ch);
        state.users = state.users.filter((u) => !nickEq(u, msg.nick));
        if (state.userModes) delete state.userModes[msg.nick];
      }
      break;
    }

    case "nick": {
      const oldNick = msg.nick;
      const newNick = msg.text;
      if (nickEq(oldNick, ownNick)) ownNick = newNick;
      for (const [ch, state] of Object.entries(chatStore.channelState)) {
        const idx = state.users.findIndex((u) => nickEq(u, oldNick));
        if (idx !== -1) {
          state.users = [...state.users];
          state.users[idx] = newNick;
          state.users.sort();
          if (state.userModes && state.userModes[oldNick] !== undefined) {
            state.userModes[newNick] = state.userModes[oldNick];
            delete state.userModes[oldNick];
          }
          addSystemMessage(ch, `${oldNick} is now known as ${newNick}`, now);
        }
      }
      break;
    }

    case "topic": {
      const ch = msg.channel;
      if (ch) {
        const state = getChannelState(ch);
        state.topic = msg.topic;
        // Only show system message for user-initiated topic changes (has nick).
        // RPL_TOPIC (332) on join has no nick — just set the topic silently.
        if (msg.nick) {
          addSystemMessage(ch, `${msg.nick} set topic: ${msg.topic}`, now);
        }
      }
      break;
    }

    case "names": {
      const ch = msg.channel;
      if (ch && msg.users) {
        const state = getChannelState(ch);
        state.users = [...msg.users].sort();
        if (msg.user_modes) {
          state.userModes = { ...msg.user_modes };
        }
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

    case "notice": {
      // Display notices as system-style messages in the relevant channel.
      const ch = msg.channel || chatStore.activeChannel;
      if (ch) {
        addSystemMessage(ch, `[${msg.nick}] ${msg.text}`, now);
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
