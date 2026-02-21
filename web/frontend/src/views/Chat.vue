<template>
  <div class="flex h-screen bg-bg-primary">
    <!-- Sidebar placeholder (Task 21) -->
    <aside class="hidden w-sidebar flex-shrink-0 border-r border-border bg-bg-secondary md:block">
      <div class="flex h-14 items-center border-b border-border px-4">
        <h2 class="font-mono text-sm font-bold text-accent">murmur</h2>
      </div>
      <div class="p-4">
        <p class="text-xs text-text-muted">Channels will appear here.</p>
      </div>
    </aside>

    <!-- Main content area -->
    <div class="flex flex-1 flex-col">
      <!-- Top bar -->
      <header class="flex h-14 items-center justify-between border-b border-border bg-bg-secondary px-4">
        <div class="flex items-center gap-2">
          <span class="font-mono text-sm text-text-secondary">#murmur</span>
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

      <!-- Message area placeholder (Task 17) -->
      <main class="flex flex-1 items-center justify-center">
        <div class="text-center">
          <p class="font-mono text-lg text-text-secondary">Chat interface</p>
          <p class="mt-1 text-sm text-text-muted">
            Message list and input will be built in Task 17.
          </p>
          <div class="mt-4 inline-flex items-center gap-2 rounded border border-border bg-bg-secondary px-3 py-2">
            <span class="h-2 w-2 rounded-full bg-success"></span>
            <span class="font-mono text-xs text-text-secondary">Connected as {{ nick }}</span>
          </div>
        </div>
      </main>
    </div>
  </div>
</template>

<script setup>
import { ref } from "vue";
import { useRouter } from "vue-router";
import { SESSION_NICK_KEY, SESSION_SIGNING_KEY, API } from "../constants.js";
import { signedFetch } from "../api.js";

const router = useRouter();
const nick = ref(sessionStorage.getItem(SESSION_NICK_KEY) || "unknown");

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
