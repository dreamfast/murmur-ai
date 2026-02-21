<template>
  <div class="flex h-screen bg-bg-primary">
    <!-- Sidebar -->
    <aside class="hidden w-sidebar flex-shrink-0 border-r border-border bg-bg-secondary md:block">
      <div class="flex h-14 items-center border-b border-border px-4">
        <h2 class="font-mono text-sm font-bold text-accent">murmur</h2>
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
        >
          <span class="text-base">&#x1F4AC;</span>
          <span class="font-mono">#murmur</span>
        </router-link>
        <router-link
          :to="{ name: 'admin' }"
          class="flex items-center gap-2 rounded px-3 py-2 text-sm transition"
          :class="
            $route.name === 'admin'
              ? 'bg-bg-hover text-text-primary'
              : 'text-text-secondary hover:bg-bg-hover hover:text-text-primary'
          "
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
        <div class="flex-1 overflow-y-auto px-3 pb-3">
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
    <div class="flex flex-1 flex-col">
      <!-- Top bar -->
      <header class="flex h-14 items-center justify-between border-b border-border bg-bg-secondary px-4">
        <div class="flex min-w-0 items-center gap-2">
          <span class="flex-shrink-0 font-mono text-sm text-text-secondary">{{ pageTitle }}</span>
          <span
            v-if="$route.name === 'chat' && chatStore.topic"
            class="truncate text-xs text-text-muted"
          >— {{ chatStore.topic }}</span>
        </div>
        <div class="flex items-center gap-3">
          <span class="font-mono text-xs text-text-muted">{{ nick }}</span>
          <button
            @click="handleLogout"
            class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
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
import { ref, computed } from "vue";
import { useRouter, useRoute } from "vue-router";
import { SESSION_NICK_KEY, SESSION_SIGNING_KEY, API } from "../constants.js";
import { signedFetch } from "../api.js";
import { chatStore } from "../stores/chatStore.js";

const router = useRouter();
const route = useRoute();
const nick = ref(sessionStorage.getItem(SESSION_NICK_KEY) || "unknown");

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
