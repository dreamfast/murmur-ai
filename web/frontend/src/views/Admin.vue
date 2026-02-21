<template>
  <div class="flex flex-1 flex-col overflow-y-auto p-6">
    <!-- Page header -->
    <div class="mb-6">
      <h1 class="font-mono text-xl font-bold text-text-primary">Admin Panel</h1>
      <p class="mt-1 text-sm text-text-muted">Manage clients, tools, and permissions.</p>
    </div>

    <!-- Loading state -->
    <div v-if="loading && !status" class="flex flex-1 items-center justify-center">
      <p class="font-mono text-sm text-text-muted">Loading...</p>
    </div>

    <!-- Error state -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >
      {{ error }}
    </div>

    <template v-else-if="status">
      <!-- Connected Clients -->
      <section class="mb-8">
        <h2 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
          Connected Clients
          <span class="ml-2 rounded bg-bg-tertiary px-1.5 py-0.5 text-xs text-text-muted">{{ status.clients }}</span>
        </h2>

        <div v-if="status.client_details && status.client_details.length > 0" class="space-y-3">
          <div
            v-for="client in status.client_details"
            :key="client.client_id"
            class="rounded-lg border border-border bg-bg-secondary p-4"
          >
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-3">
                <span class="h-2 w-2 rounded-full bg-success"></span>
                <span class="font-mono text-sm font-bold text-text-primary">{{ client.client_id }}</span>
                <span class="text-xs text-text-muted">{{ client.hostname }}</span>
              </div>
              <span
                class="rounded px-2 py-0.5 text-xs font-medium"
                :class="{
                  'bg-success/20 text-success': client.autonomy === 'auto',
                  'bg-warning/20 text-warning': client.autonomy === 'approve',
                  'bg-info/20 text-info': client.autonomy === 'report',
                }"
              >{{ client.autonomy }}</span>
            </div>
            <div v-if="client.tools && client.tools.length > 0" class="mt-3 flex flex-wrap gap-1.5">
              <span
                v-for="tool in client.tools"
                :key="tool"
                class="rounded bg-bg-tertiary px-2 py-0.5 font-mono text-xs text-text-secondary"
              >{{ tool }}</span>
            </div>
          </div>
        </div>

        <div v-else class="rounded-lg border border-border bg-bg-secondary p-4 text-center">
          <p class="text-sm text-text-muted">No clients connected.</p>
        </div>
      </section>

      <!-- Quick Actions -->
      <section class="mb-8">
        <h2 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
          Quick Actions
        </h2>
        <p class="mb-3 text-xs text-text-muted">
          These actions send commands to the IRC channel. Results appear in the chat.
        </p>
        <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          <button
            v-for="action in quickActions"
            :key="action.cmd"
            @click="sendCommand(action.cmd)"
            class="flex items-center gap-2 rounded-lg border border-border bg-bg-secondary px-4 py-3 text-left transition hover:border-accent/50 hover:bg-bg-hover"
          >
            <span class="text-lg">{{ action.icon }}</span>
            <div>
              <div class="font-mono text-sm text-text-primary">{{ action.label }}</div>
              <div class="text-xs text-text-muted">{{ action.desc }}</div>
            </div>
          </button>
        </div>
      </section>

      <!-- Server Info -->
      <section>
        <h2 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
          Server Info
        </h2>
        <div class="rounded-lg border border-border bg-bg-secondary">
          <div
            v-for="(item, idx) in serverInfo"
            :key="item.label"
            class="flex items-center justify-between px-4 py-3"
            :class="idx > 0 ? 'border-t border-border' : ''"
          >
            <span class="text-sm text-text-muted">{{ item.label }}</span>
            <span class="font-mono text-sm text-text-primary">{{ item.value }}</span>
          </div>
        </div>
      </section>
    </template>

    <!-- Auto-refresh indicator -->
    <div v-if="status" class="mt-6 flex items-center gap-2">
      <span class="h-1.5 w-1.5 rounded-full bg-success"></span>
      <span class="text-xs text-text-muted">Auto-refreshes every 15s</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useRouter } from "vue-router";
import { signedFetch } from "../api.js";
import { API } from "../constants.js";

const REFRESH_INTERVAL_MS = 15_000;

const router = useRouter();
const status = ref(null);
const loading = ref(false);
const error = ref("");
let refreshTimer = null;

const quickActions = [
  { cmd: "!status", label: "!status", desc: "Server status summary", icon: "\u{1F4CA}" },
  { cmd: "!clients", label: "!clients", desc: "List connected clients", icon: "\u{1F4E1}" },
  { cmd: "!tools", label: "!tools", desc: "List available tools", icon: "\u{1F527}" },
  { cmd: "!tasks", label: "!tasks", desc: "List scheduled tasks", icon: "\u{23F0}" },
  { cmd: "!pending", label: "!pending", desc: "Show pending approvals", icon: "\u{2705}" },
  { cmd: "!reload", label: "!reload", desc: "Reload configuration", icon: "\u{1F504}" },
];

const serverInfo = computed(() => {
  if (!status.value) return [];
  return [
    { label: "Server Name", value: status.value.server_name || "—" },
    { label: "LLM Provider", value: status.value.provider || "—" },
    { label: "Uptime", value: status.value.uptime || "—" },
    { label: "Total Tools", value: String(status.value.tools ?? 0) },
    { label: "Connected Clients", value: String(status.value.clients ?? 0) },
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
        // ignore
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

/** Send a command by navigating to chat and pre-filling the command. */
function sendCommand(cmd) {
  // Navigate to chat view — the command will need to be typed manually
  // since we can't inject into the WebSocket from here. Show the command
  // in the URL hash as a hint.
  router.push({ name: "chat", query: { cmd } });
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
