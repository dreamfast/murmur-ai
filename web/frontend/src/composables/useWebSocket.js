/**
 * WebSocket composable for the Murmur dashboard.
 *
 * Manages a single WebSocket connection to the IRC bridge, dispatching
 * incoming messages to reactive state. Handles reconnection with
 * exponential backoff and connection status tracking.
 */

import { ref, shallowRef, onUnmounted } from "vue";
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

/**
 * Reactive WebSocket connection to the IRC bridge.
 *
 * @returns {{ state, messages, users, topic, connect, disconnect, send }}
 */
export function useWebSocket() {
  const state = ref(WS_STATE.DISCONNECTED);
  const messages = ref([]);
  const users = ref([]);
  const topic = ref("");

  /** @type {import('vue').ShallowRef<WebSocket|null>} */
  const ws = shallowRef(null);
  let reconnectAttempts = 0;
  let reconnectTimer = null;
  let intentionalClose = false;

  /**
   * Connect to the WebSocket bridge.
   * @param {string} channel — the IRC channel to track (for filtering)
   */
  async function connect(channel) {
    intentionalClose = false;
    state.value = WS_STATE.CONNECTING;

    try {
      const url = await signedWebSocketURL(API.WS);
      const socket = new WebSocket(url);
      ws.value = socket;

      socket.onopen = () => {
        reconnectAttempts = 0;
        // State will be set to CONNECTED when we receive the "connected" message
        // from the bridge (after IRC handshake completes).
      };

      socket.onmessage = (event) => {
        let msg;
        try {
          msg = JSON.parse(event.data);
        } catch {
          return;
        }
        handleMessage(msg, channel);
      };

      socket.onclose = () => {
        ws.value = null;
        if (!intentionalClose) {
          state.value = WS_STATE.DISCONNECTED;
          scheduleReconnect(channel);
        }
      };

      socket.onerror = () => {
        state.value = WS_STATE.ERROR;
      };
    } catch {
      state.value = WS_STATE.ERROR;
      scheduleReconnect(channel);
    }
  }

  /** Disconnect intentionally (no reconnect). */
  function disconnect() {
    intentionalClose = true;
    clearReconnectTimer();
    if (ws.value) {
      ws.value.close();
      ws.value = null;
    }
    state.value = WS_STATE.DISCONNECTED;
  }

  /**
   * Send a message to the IRC channel via the WebSocket bridge.
   * @param {string} channel
   * @param {string} text
   */
  function send(channel, text) {
    if (ws.value && ws.value.readyState === WebSocket.OPEN) {
      ws.value.send(JSON.stringify({ type: "message", channel, text }));
    }
  }

  /** Handle an incoming WebSocket message from the bridge. */
  function handleMessage(msg, channel) {
    const now = Date.now();

    switch (msg.type) {
      case "connected":
        state.value = WS_STATE.CONNECTED;
        addSystemMessage(`Connected to IRC as ${msg.nick}`, now);
        break;

      case "message":
        messages.value = [
          ...messages.value,
          {
            id: now + Math.random(),
            type: "message",
            nick: msg.nick,
            text: msg.text,
            channel: msg.channel,
            time: now,
          },
        ];
        break;

      case "join":
        if (msg.channel === channel) {
          addSystemMessage(`${msg.nick} joined ${msg.channel}`, now);
          // Add to user list if not present.
          if (!users.value.includes(msg.nick)) {
            users.value = [...users.value, msg.nick].sort();
          }
        }
        break;

      case "part":
        if (msg.channel === channel) {
          const reason = msg.text ? ` (${msg.text})` : "";
          addSystemMessage(`${msg.nick} left ${msg.channel}${reason}`, now);
          users.value = users.value.filter((u) => u !== msg.nick);
        }
        break;

      case "topic":
        if (msg.channel === channel) {
          topic.value = msg.topic;
          addSystemMessage(
            `${msg.nick} set topic: ${msg.topic}`,
            now,
          );
        }
        break;

      case "names":
        if (msg.channel === channel && msg.users) {
          users.value = [...msg.users].sort();
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
        state.value = WS_STATE.ERROR;
        addSystemMessage(`Error: ${msg.error}`, now);
        break;
    }
  }

  /** Add a system/event message to the message list. */
  function addSystemMessage(text, time) {
    messages.value = [
      ...messages.value,
      {
        id: time + Math.random(),
        type: "system",
        text,
        time,
      },
    ];
  }

  /** Schedule a reconnect with exponential backoff. */
  function scheduleReconnect(channel) {
    if (intentionalClose || reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      return;
    }
    const delay = Math.min(
      BASE_RECONNECT_DELAY * Math.pow(2, reconnectAttempts),
      MAX_RECONNECT_DELAY,
    );
    reconnectAttempts++;
    reconnectTimer = setTimeout(() => connect(channel), delay);
  }

  /** Clear any pending reconnect timer. */
  function clearReconnectTimer() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  // Clean up on component unmount.
  onUnmounted(() => {
    disconnect();
  });

  return {
    /** Reactive connection state (WS_STATE enum). */
    state,
    /** Reactive array of chat messages. */
    messages,
    /** Reactive sorted array of nicks in the channel. */
    users,
    /** Reactive channel topic string. */
    topic,
    /** Connect to the WebSocket bridge. */
    connect,
    /** Disconnect from the WebSocket bridge. */
    disconnect,
    /** Send a message to a channel. */
    send,
  };
}
