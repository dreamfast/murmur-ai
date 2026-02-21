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
        <router-link
          :to="{ name: 'chat' }"
          class="flex items-center gap-2 rounded px-3 py-2 text-sm transition"
          :class="
            $route.name === 'chat'
              ? 'bg-bg-hover text-text-primary'
              : 'text-text-secondary hover:bg-bg-hover hover:text-text-primary'
          "
          @click="sidebarOpen = false"
        >
          <span class="text-base">&#x1F4AC;</span>
          <span class="font-mono">#murmur</span>
          <!-- Unread message badge -->
          <span
            v-if="chatStore.unreadCount > 0 && $route.name !== 'chat'"
            class="ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-accent px-1.5 text-xs font-bold text-white"
          >{{ chatStore.unreadCount > 99 ? '99+' : chatStore.unreadCount }}</span>
        </router-link>
        <router-link
          :to="{ name: 'admin' }"
          class="flex items-center gap-2 rounded px-3 py-2 text-sm transition"
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
      <!-- User list (visible when on chat route) -->
      <div v-if="$route.name === 'chat' && chatStore.users.length > 0" class="flex flex-1 flex-col border-t border-border">
        <div class="flex items-center justify-between px-3 py-2">
          <span class="text-xs font-medium uppercase tracking-wider text-text-muted">Users</span>
          <span class="rounded bg-bg-tertiary px-1.5 py-0.5 text-xs text-text-muted">{{ chatStore.users.length }}</span>
        </div>
        <div class="max-h-48 overflow-y-auto px-3 pb-3 md:max-h-none md:flex-1">
          <div
            v-for="user in chatStore.users"
            :key="user"
            class="flex items-center gap-2 rounded px-2 py-1 text-sm"
          >
            <span class="h-1.5 w-1.5 rounded-full bg-success"></span>
            <span class="truncate font-mono text-xs text-text-secondary">{{ user }}</span>
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
            v-if="$route.name === 'chat' && chatStore.topic"
            class="hidden truncate text-xs text-text-muted sm:inline"
          >— {{ chatStore.topic }}</span>
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
import { chatStore, wsConnect, wsDisconnect, clearUnread } from "../stores/chatStore.js";

const router = useRouter();
const route = useRoute();
const nick = ref(sessionStorage.getItem(SESSION_NICK_KEY) || "unknown");
const sidebarOpen = ref(false);

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

const pageTitle = computed(() => {
  switch (route.name) {
    case "overview":
      return "Overview";
    case "chat":
      return "#murmur";
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
