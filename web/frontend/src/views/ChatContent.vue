<template>
  <div class="flex flex-1 flex-col overflow-hidden">
    <!-- Connection status bar -->
    <div
      v-if="wsState !== WS_STATE.CONNECTED"
      class="flex items-center gap-2 border-b border-border px-4 py-2 text-xs"
      :class="{
        'bg-warning/10 text-warning': wsState === WS_STATE.CONNECTING,
        'bg-error/10 text-error': wsState === WS_STATE.ERROR,
        'bg-bg-tertiary text-text-muted': wsState === WS_STATE.DISCONNECTED,
      }"
    >
      <span
        class="h-2 w-2 rounded-full"
        :class="{
          'bg-warning animate-pulse': wsState === WS_STATE.CONNECTING,
          'bg-error': wsState === WS_STATE.ERROR,
          'bg-text-muted': wsState === WS_STATE.DISCONNECTED,
        }"
      ></span>
      <span v-if="wsState === WS_STATE.CONNECTING">Connecting to IRC...</span>
      <span v-else-if="wsState === WS_STATE.ERROR">Connection error. Retrying...</span>
      <span v-else>Disconnected</span>
    </div>

    <!-- Message list -->
    <div ref="messageListRef" class="flex-1 overflow-y-auto px-4 py-3">
      <div v-if="messages.length === 0" class="flex h-full items-center justify-center">
        <p class="text-sm text-text-muted">
          {{ wsState === WS_STATE.CONNECTED ? "No messages yet." : "Waiting for connection..." }}
        </p>
      </div>
      <div v-else class="space-y-0.5">
        <div
          v-for="msg in messages"
          :key="msg.id"
          class="group flex gap-2 rounded px-2 py-0.5 hover:bg-bg-secondary/50"
        >
          <!-- Timestamp -->
          <span class="flex-shrink-0 font-mono text-xs leading-6 text-text-muted opacity-0 group-hover:opacity-100 transition">
            {{ formatTime(msg.time) }}
          </span>

          <!-- System message -->
          <template v-if="msg.type === 'system'">
            <span class="text-xs leading-6 italic text-text-muted">{{ msg.text }}</span>
          </template>

          <!-- Chat message -->
          <template v-else>
            <span
              class="flex-shrink-0 font-mono text-sm font-bold leading-6"
              :style="{ color: nickColor(msg.nick) }"
            >{{ msg.nick }}</span>
            <!-- eslint-disable-next-line vue/no-v-html -- renderMessage escapes HTML before adding formatting -->
            <span class="min-w-0 break-words font-mono text-sm leading-6 text-text-primary" v-html="renderMessage(msg)"></span>
          </template>
        </div>
      </div>
    </div>

    <!-- Input area -->
    <div class="border-t border-border bg-bg-secondary px-4 py-3">
      <form @submit.prevent="handleSend" class="flex gap-2">
        <input
          ref="inputRef"
          v-model="inputText"
          type="text"
          :disabled="wsState !== WS_STATE.CONNECTED"
          :placeholder="wsState === WS_STATE.CONNECTED ? `Message ${channel}` : 'Connecting...'"
          class="flex-1 rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary placeholder-text-muted outline-none transition focus:border-border-focus focus:ring-1 focus:ring-accent/50 disabled:opacity-50"
          @keydown.up="handleHistoryUp"
          @keydown.down="handleHistoryDown"
        />
        <button
          type="submit"
          :disabled="wsState !== WS_STATE.CONNECTED || !inputText.trim()"
          class="rounded bg-accent px-4 py-2 text-sm font-medium text-white transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          Send
        </button>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, nextTick, onMounted } from "vue";
import { SESSION_NICK_KEY } from "../constants.js";
import { useWebSocket, WS_STATE } from "../composables/useWebSocket.js";
import { parseIRCColors, stripIRCColors } from "../utils/ircColors.js";
import { renderMarkdown } from "../utils/markdown.js";

const nick = sessionStorage.getItem(SESSION_NICK_KEY) || "unknown";
const channel = "#murmur";

const { state: wsState, messages, users, topic, connect, send } = useWebSocket();

const inputText = ref("");
const inputRef = ref(null);
const messageListRef = ref(null);

// Input history (up/down arrow to recall previous messages).
const inputHistory = ref([]);
const historyIndex = ref(-1);

/** Send a message to the channel. */
function handleSend() {
  const text = inputText.value.trim();
  if (!text) return;

  send(channel, text);

  // Add own message to the display immediately (IRC will echo it back
  // via the bridge, but we show it instantly for responsiveness).
  messages.value = [
    ...messages.value,
    {
      id: Date.now() + Math.random(),
      type: "message",
      nick,
      text,
      channel,
      time: Date.now(),
    },
  ];

  // Save to input history.
  inputHistory.value = [...inputHistory.value, text];
  historyIndex.value = -1;
  inputText.value = "";
}

/** Navigate input history (up arrow). */
function handleHistoryUp() {
  if (inputHistory.value.length === 0) return;
  if (historyIndex.value === -1) {
    historyIndex.value = inputHistory.value.length - 1;
  } else if (historyIndex.value > 0) {
    historyIndex.value--;
  }
  inputText.value = inputHistory.value[historyIndex.value];
}

/** Navigate input history (down arrow). */
function handleHistoryDown() {
  if (historyIndex.value === -1) return;
  if (historyIndex.value < inputHistory.value.length - 1) {
    historyIndex.value++;
    inputText.value = inputHistory.value[historyIndex.value];
  } else {
    historyIndex.value = -1;
    inputText.value = "";
  }
}

/**
 * Render a message for display. Bot messages get markdown rendering;
 * other messages get IRC color parsing. The heuristic: if the message
 * contains markdown patterns (code fences, **bold**, links) and no IRC
 * control characters, use markdown. Otherwise use IRC color parsing.
 */
function renderMessage(msg) {
  const text = msg.text || "";
  // Check for IRC control characters (0x02, 0x03, 0x0F, 0x16, 0x1D, 0x1F).
  const hasIRCCodes = /[\x02\x03\x0f\x16\x1d\x1f]/.test(text);
  if (hasIRCCodes) {
    return parseIRCColors(text);
  }
  // Check for markdown patterns.
  const hasMarkdown = /```|`[^`]+`|\*\*|\[.+\]\(.+\)|^#{1,3}\s|^>\s|^[-*]\s|^\d+\.\s/m.test(text);
  if (hasMarkdown) {
    return renderMarkdown(text);
  }
  // Plain text — still render markdown for auto-linking URLs.
  return renderMarkdown(text);
}

/** Format a timestamp for display (HH:MM). */
function formatTime(ts) {
  const d = new Date(ts);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
}

/**
 * Generate a consistent color for a nick. Uses a simple hash to pick
 * from a palette of distinct colors that work on dark backgrounds.
 */
const NICK_COLORS = [
  "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd",
  "#56b6c2", "#d19a66", "#be5046", "#7ec8e3", "#c3e88d",
  "#f78c6c", "#89ddff", "#ffcb6b", "#c792ea", "#f07178",
  "#82aaff",
];

function nickColor(name) {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = name.charCodeAt(i) + ((hash << 5) - hash);
  }
  return NICK_COLORS[Math.abs(hash) % NICK_COLORS.length];
}

// Auto-scroll to bottom when new messages arrive.
watch(
  () => messages.value.length,
  async () => {
    await nextTick();
    const el = messageListRef.value;
    if (el) {
      // Only auto-scroll if user is near the bottom (within 150px).
      const isNearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 150;
      if (isNearBottom) {
        el.scrollTop = el.scrollHeight;
      }
    }
  },
);

// Connect on mount.
onMounted(() => {
  connect(channel);
  // Focus the input when connected.
  if (inputRef.value) {
    inputRef.value.focus();
  }
});

// Expose for parent layout (Chat.vue) if needed.
defineExpose({ users, topic });
</script>
