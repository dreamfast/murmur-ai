<template>
  <div class="flex flex-1 flex-col overflow-y-auto p-6">
    <!-- Page header -->
    <div class="mb-6">
      <h1 class="font-mono text-xl font-bold text-text-primary">Overview</h1>
      <p class="mt-1 text-sm text-text-muted">Server status and system information.</p>
    </div>

    <!-- Loading state -->
    <div v-if="loading && !status" class="flex flex-1 items-center justify-center">
      <p class="font-mono text-sm text-text-muted">Loading status...</p>
    </div>

    <!-- Error state -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >
      {{ error }}
    </div>

    <!-- Status cards -->
    <div v-else-if="status" class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="card in cards"
        :key="card.label"
        class="rounded-lg border border-border bg-bg-secondary p-4"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg text-lg"
            :class="card.iconBg"
          >
            {{ card.icon }}
          </div>
          <div class="min-w-0 flex-1">
            <p class="text-xs font-medium uppercase tracking-wider text-text-muted">
              {{ card.label }}
            </p>
            <p class="truncate font-mono text-lg font-bold text-text-primary">
              {{ card.value }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- Auto-refresh indicator -->
    <div v-if="status" class="mt-6 flex items-center gap-2">
      <span class="h-1.5 w-1.5 rounded-full bg-success"></span>
      <span class="text-xs text-text-muted">
        Auto-refreshes every {{ refreshIntervalSec }}s
      </span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue";
import { signedFetch } from "../api.js";
import { API } from "../constants.js";

const REFRESH_INTERVAL_MS = 30_000;
const refreshIntervalSec = REFRESH_INTERVAL_MS / 1000;

const status = ref(null);
const loading = ref(false);
const error = ref("");

let refreshTimer = null;

const cards = computed(() => {
  if (!status.value) return [];
  return [
    {
      label: "Server",
      value: status.value.server_name || "murmur",
      icon: "\u{1F5A5}",
      iconBg: "bg-accent/20 text-accent",
    },
    {
      label: "Uptime",
      value: status.value.uptime || "—",
      icon: "\u{23F1}",
      iconBg: "bg-success/20 text-success",
    },
    {
      label: "LLM Provider",
      value: status.value.provider || "—",
      icon: "\u{1F9E0}",
      iconBg: "bg-info/20 text-info",
    },
    {
      label: "Clients",
      value: String(status.value.clients ?? 0),
      icon: "\u{1F4E1}",
      iconBg: "bg-warning/20 text-warning",
    },
    {
      label: "Tools",
      value: String(status.value.tools ?? 0),
      icon: "\u{1F527}",
      iconBg: "bg-accent-muted/20 text-accent-muted",
    },
  ];
});

async function fetchStatus() {
  loading.value = true;
  error.value = "";

  try {
    const res = await signedFetch(API.STATUS);

    if (!res.ok) {
      let msg = `Server returned ${res.status}`;
      try {
        const data = await res.json();
        if (data.error) msg = data.error;
      } catch {
        // ignore parse errors
      }
      error.value = msg;
      return;
    }

    status.value = await res.json();
  } catch {
    error.value = "Network error — is the server running?";
  } finally {
    loading.value = false;
  }
}

onMounted(() => {
  fetchStatus();
  refreshTimer = setInterval(fetchStatus, REFRESH_INTERVAL_MS);
});

onUnmounted(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
});
</script>
