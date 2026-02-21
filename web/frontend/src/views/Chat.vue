<template>
  <div class="flex h-screen bg-bg-primary">
    <!-- Mobile sidebar overlay -->
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-30 bg-black/50 md:hidden"
      @click="sidebarOpen = false"
    ></div>

    <!-- Sidebar -->
    <aside
      class="fixed inset-y-0 left-0 z-40 w-sidebar flex-shrink-0 border-r border-border bg-bg-secondary transition-transform duration-200 md:static md:translate-x-0"
      :class="sidebarOpen ? 'translate-x-0' : '-translate-x-full'"
    >
      <div class="flex h-14 items-center justify-between border-b border-border px-4">
        <h2 class="font-mono text-sm font-bold text-accent">murmur</h2>
        <!-- Close button (mobile only) -->
        <button
          aria-label="Close sidebar"
          class="rounded p-1 text-text-muted transition hover:bg-bg-hover hover:text-text-primary md:hidden"
          @click="sidebarOpen = false"
        >
          &#x2715;
        </button>
      </div>
      <nav class="flex flex-col gap-1 p-3">
        <router-link
          :to="{ name: 'overview' }"
          class="flex items-center gap-2 rounded px-3 py-2 text-sm transition"
          :class="
            $route.name === 'overview'
              ? 'bg-bg-hover text-text-primary'
              : 'text-text-secondary hover:bg-bg-hover hover:text-text-primary'
          "
          @click="sidebarOpen = false"
        >
          <span class="text-base">&#x1F4CA;</span>
          <span class="font-mono">Overview</span>
        </router-link>

        <!-- Channel list -->
        <div class="mt-2 mb-1 px-3 text-xs font-medium uppercase tracking-wider text-text-muted">Channels</div>
        <button
          v-for="ch in chatStore.channels"
          :key="ch"
          class="flex items-center gap-2 rounded px-3 py-1.5 text-sm transition"
          :class="
            $route.name === 'chat' && chatStore.activeChannel === ch
              ? 'bg-bg-hover text-text-primary'
              : 'text-text-secondary hover:bg-bg-hover hover:text-text-primary'
          "
          @click="switchChannel(ch)"
        >
          <span class="font-mono text-xs">#</span>
          <span class="truncate font-mono">{{ ch.replace(/^#/, '') }}</span>
          <!-- Per-channel unread badge -->
          <span
            v-if="channelUnread(ch) > 0 && chatStore.activeChannel !== ch"
            class="ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-accent px-1.5 text-xs font-bold text-white"
          >{{ channelUnread(ch) > 99 ? '99+' : channelUnread(ch) }}</span>
        </button>

        <!-- Join channel input -->
        <form class="mt-1 px-1" @submit.prevent="handleJoinChannel">
          <input
            v-model="joinChannelInput"
            type="text"
            placeholder="Join #channel..."
            class="w-full rounded border border-border bg-bg-input px-2 py-1 font-mono text-xs text-text-primary placeholder-text-muted outline-none transition focus:border-border-focus"
          />
        </form>

        <!-- Admin link -->
        <router-link
          :to="{ name: 'admin' }"
          class="mt-2 flex items-center gap-2 rounded px-3 py-2 text-sm transition"
          :class="
            $route.name === 'admin'
              ? 'bg-bg-hover text-text-primary'
              : 'text-text-secondary hover:bg-bg-hover hover:text-text-primary'
          "
          @click="sidebarOpen = false"
        >
          <span class="text-base">&#x2699;</span>
          <span class="font-mono">Admin</span>
        </router-link>
      </nav>
      <!-- User list for active channel (visible when on chat route) -->
      <div v-if="$route.name === 'chat' && activeUsers.length > 0" class="flex flex-1 flex-col border-t border-border">
        <div class="flex items-center justify-between px-3 py-2">
          <span class="text-xs font-medium uppercase tracking-wider text-text-muted">Users</span>
          <span class="rounded bg-bg-tertiary px-1.5 py-0.5 text-xs text-text-muted">{{ activeUsers.length }}</span>
        </div>
        <div class="max-h-48 overflow-y-auto px-3 pb-3 md:max-h-none md:flex-1">
          <div
            v-for="user in activeUsers"
            :key="user.nick"
            class="flex items-center gap-1 rounded px-2 py-1 text-sm"
          >
            <span
              v-if="user.prefix"
              class="w-3 flex-shrink-0 text-center font-mono text-xs font-bold"
              :class="user.color"
            >{{ user.prefix }}</span>
            <span
              v-else
              class="w-3 flex-shrink-0"
            ></span>
            <span class="truncate font-mono text-xs text-text-secondary">{{ user.nick }}</span>
          </div>
        </div>
      </div>
    </aside>

    <!-- Main content area -->
    <div class="flex flex-1 flex-col overflow-hidden">
      <!-- Top bar -->
      <header class="flex h-14 items-center justify-between border-b border-border bg-bg-secondary px-4">
        <div class="flex min-w-0 items-center gap-2">
          <!-- Hamburger menu (mobile only) -->
          <button
            aria-label="Toggle sidebar"
            class="mr-1 rounded p-1.5 text-text-muted transition hover:bg-bg-hover hover:text-text-primary md:hidden"
            @click="sidebarOpen = !sidebarOpen"
          >
            <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
          <span class="flex-shrink-0 font-mono text-sm text-text-secondary">{{ pageTitle }}</span>
          <span
            v-if="$route.name === 'chat' && activeTopic"
            class="hidden truncate text-xs text-text-muted sm:inline"
          >— {{ activeTopic }}</span>
        </div>
        <div class="flex items-center gap-3">
          <span class="hidden font-mono text-xs text-text-muted sm:inline">{{ nick }}</span>
          <button
            class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
            @click="handleLogout"
          >
            Logout
          </button>
        </div>
      </header>

      <!-- Nested route content (Overview, Chat, etc.) -->
      <router-view />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRouter, useRoute } from "vue-router";
import { SESSION_NICK_KEY, SESSION_SIGNING_KEY, API } from "../constants.js";
import { signedFetch } from "../api.js";
import { chatStore, wsConnect, wsDisconnect, clearUnread, setActiveChannel, wsJoin } from "../stores/chatStore.js";

const router = useRouter();
const route = useRoute();
const nick = ref(sessionStorage.getItem(SESSION_NICK_KEY) || "unknown");
const sidebarOpen = ref(false);
const joinChannelInput = ref("");

// Track the previous message count to detect new messages for unread tracking.
let lastSeenMessageCount = 0;

// Connect WebSocket when the layout mounts (persists across child routes).
onMounted(() => {
  wsConnect();
  lastSeenMessageCount = chatStore.messages.length;
});

// Disconnect WebSocket when the layout unmounts (logout / session end).
onUnmounted(() => {
  wsDisconnect();
});

// Close sidebar on route change (mobile).
watch(() => route.name, (newName) => {
  sidebarOpen.value = false;
  // Clear unread count when navigating to chat.
  if (newName === "chat") {
    clearUnread();
    lastSeenMessageCount = chatStore.messages.length;
  }
});

// Track unread messages: increment when new messages arrive and user is NOT on chat.
watch(
  () => chatStore.messages.length,
  (newLen) => {
    if (route.name === "chat") {
      // User is viewing chat — no unread tracking, just update the marker.
      lastSeenMessageCount = newLen;
      return;
    }
    // Count only actual chat messages (not system messages) as unread.
    const newMessages = chatStore.messages.slice(lastSeenMessageCount);
    const chatMessages = newMessages.filter((m) => m.type === "message");
    if (chatMessages.length > 0) {
      chatStore.unreadCount += chatMessages.length;
    }
    lastSeenMessageCount = newLen;
  },
);

/** Get unread count for a specific channel. */
function channelUnread(ch) {
  const state = chatStore.channelState[ch];
  return state ? state.unread : 0;
}

/**
 * Rank order for IRC mode prefixes. Lower index = higher rank.
 * Used for sorting users in the sidebar.
 */
const MODE_RANK = ["~", "&", "@", "%", "+"];

/**
 * Color classes for each IRC mode prefix.
 * Owner(~) = gold, Admin(&) = purple, Op(@) = green, HalfOp(%) = teal, Voice(+) = blue.
 */
const PREFIX_COLORS = {
  "~": "text-amber-400",
  "&": "text-purple-400",
  "@": "text-green-400",
  "%": "text-teal-400",
  "+": "text-blue-400",
};

/** Rank value for users without a mode prefix (sorted after all prefixed users). */
const UNRANKED = MODE_RANK.length;

/**
 * Users in the active channel, sorted by rank then alphabetically.
 * Owner(~) > Admin(&) > Op(@) > HalfOp(%) > Voice(+) > regular.
 * Returns enriched objects { nick, prefix, color } to avoid repeated lookups in the template.
 */
const activeUsers = computed(() => {
  const state = chatStore.channelState[chatStore.activeChannel];
  if (!state || !state.users) return [];
  const modes = state.userModes || {};
  return state.users
    .map((nick) => {
      const prefix = modes[nick] || "";
      const rank = MODE_RANK.indexOf(prefix);
      return {
        nick,
        prefix,
        color: PREFIX_COLORS[prefix] || "",
        rank: rank === -1 ? UNRANKED : rank,
      };
    })
    .sort((a, b) => {
      if (a.rank !== b.rank) return a.rank - b.rank;
      return a.nick.localeCompare(b.nick, undefined, { sensitivity: "base" });
    });
});

/** Topic of the active channel. */
const activeTopic = computed(() => {
  const state = chatStore.channelState[chatStore.activeChannel];
  return state ? state.topic : "";
});

/** Switch to a channel and navigate to chat view. */
function switchChannel(ch) {
  setActiveChannel(ch);
  sidebarOpen.value = false;
  if (route.name !== "chat") {
    router.push({ name: "chat" });
  }
}

/** Join a new channel from the input. */
function handleJoinChannel() {
  let ch = joinChannelInput.value.trim();
  if (!ch) return;
  // Ensure channel starts with #.
  if (!ch.startsWith("#")) {
    ch = "#" + ch;
  }
  wsJoin(ch);
  setActiveChannel(ch);
  joinChannelInput.value = "";
  sidebarOpen.value = false;
  if (route.name !== "chat") {
    router.push({ name: "chat" });
  }
}

const pageTitle = computed(() => {
  switch (route.name) {
    case "overview":
      return "Overview";
    case "chat":
      return chatStore.activeChannel || "#murmur";
    case "admin":
      return "Admin Panel";
    default:
      return "murmur";
  }
});

async function handleLogout() {
  try {
    await signedFetch(API.LOGOUT, { method: "POST" });
  } catch {
    // Ignore network errors on logout — clear local state regardless.
  }
  sessionStorage.removeItem(SESSION_NICK_KEY);
  sessionStorage.removeItem(SESSION_SIGNING_KEY);
  router.push({ name: "login" });
}
</script>
