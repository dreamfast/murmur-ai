<template>
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
</template>

<script setup>
import { SESSION_NICK_KEY } from "../constants.js";

const nick = sessionStorage.getItem(SESSION_NICK_KEY) || "unknown";
</script>
