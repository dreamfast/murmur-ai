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
      </nav>
      <!-- Channel list placeholder (Task 21) -->
      <div class="border-t border-border p-3">
        <p class="text-xs text-text-muted">More channels in Task 21.</p>
      </div>
    </aside>

    <!-- Main content area -->
    <div class="flex flex-1 flex-col">
      <!-- Top bar -->
      <header class="flex h-14 items-center justify-between border-b border-border bg-bg-secondary px-4">
        <div class="flex items-center gap-2">
          <span class="font-mono text-sm text-text-secondary">{{ pageTitle }}</span>
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

const router = useRouter();
const route = useRoute();
const nick = ref(sessionStorage.getItem(SESSION_NICK_KEY) || "unknown");

const pageTitle = computed(() => {
  switch (route.name) {
    case "overview":
      return "Overview";
    case "chat":
      return "#murmur";
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
