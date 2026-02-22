<template>
  <div class="p-6">
    <h2 class="mb-4 font-mono text-lg font-bold text-text-primary">Channel Settings</h2>

    <!-- Loading -->
    <div v-if="loading" class="py-8 text-center font-mono text-sm text-text-muted">Loading...</div>

    <!-- Error -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >{{ error }}</div>

    <!-- Channels table -->
    <div v-else class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full text-left text-sm">
        <thead class="border-b border-border bg-bg-tertiary">
          <tr>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Channel</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Provider</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Auto-Join</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Topic Prefix</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="ch in channels"
            :key="ch.channel"
            class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
          >
            <td class="px-4 py-2.5 font-mono text-text-primary">{{ ch.channel }}</td>
            <td class="px-4 py-2.5">
              <template v-if="editingChannel === ch.channel">
                <select
                  v-model="editForm.provider"
                  class="w-full rounded border border-border bg-bg-input px-2 py-1 font-mono text-xs text-text-primary outline-none focus:border-border-focus"
                >
                  <option value="">default</option>
                  <option v-for="p in providers" :key="p.name" :value="p.name">{{ p.name }}</option>
                </select>
              </template>
              <template v-else>
                <span class="font-mono text-xs text-text-secondary">{{ ch.provider || 'default' }}</span>
              </template>
            </td>
            <td class="px-4 py-2.5">
              <template v-if="editingChannel === ch.channel">
                <input
                  v-model="editForm.auto_join"
                  type="checkbox"
                  class="h-4 w-4 rounded border-border bg-bg-input accent-accent"
                />
              </template>
              <template v-else>
                <span
                  class="rounded px-2 py-0.5 text-xs font-medium"
                  :class="ch.auto_join ? 'bg-success/20 text-success' : 'bg-bg-tertiary text-text-muted'"
                >{{ ch.auto_join ? 'yes' : 'no' }}</span>
              </template>
            </td>
            <td class="px-4 py-2.5">
              <template v-if="editingChannel === ch.channel">
                <input
                  v-model="editForm.topic_prefix"
                  type="text"
                  class="w-full rounded border border-border bg-bg-input px-2 py-1 font-mono text-xs text-text-primary outline-none focus:border-border-focus"
                  placeholder="optional prefix"
                />
              </template>
              <template v-else>
                <span class="font-mono text-xs text-text-muted">{{ ch.topic_prefix || '—' }}</span>
              </template>
            </td>
            <td class="px-4 py-2.5">
              <template v-if="editingChannel === ch.channel">
                <div class="flex gap-1">
                  <button
                    class="rounded bg-accent px-2 py-1 text-xs text-white transition hover:bg-accent-hover disabled:opacity-50"
                    :disabled="saving"
                    @click="saveChannel(ch)"
                  >{{ saving ? '...' : 'Save' }}</button>
                  <button
                    class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover"
                    @click="cancelEdit"
                  >Cancel</button>
                </div>
              </template>
              <template v-else>
                <button
                  class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
                  @click="startEdit(ch)"
                >Edit</button>
              </template>
            </td>
          </tr>
          <tr v-if="channels.length === 0">
            <td colspan="5" class="px-4 py-8 text-center text-sm text-text-muted">No channel settings configured.</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Save error -->
    <div
      v-if="saveError"
      class="mt-3 rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >{{ saveError }}</div>
  </div>
</template>

<script setup>
import { ref, onMounted } from "vue";
import {
  adminListChannels,
  adminUpdateChannel,
  adminListProviders,
} from "../../api.js";

const channels = ref([]);
const providers = ref([]);
const loading = ref(false);
const error = ref("");
const saving = ref(false);
const saveError = ref("");
const editingChannel = ref(null);

const editForm = ref({
  provider: "",
  auto_join: false,
  topic_prefix: "",
});

async function fetchChannels() {
  loading.value = true;
  error.value = "";
  try {
    const res = await adminListChannels();
    if (res.ok) {
      channels.value = res.data || [];
    } else {
      error.value = res.error || "Failed to load channels";
    }
  } catch {
    error.value = "Network error";
  } finally {
    loading.value = false;
  }
}

async function fetchProviders() {
  try {
    const res = await adminListProviders();
    if (res.ok) {
      providers.value = res.data || [];
    }
  } catch {
    // Non-critical — provider dropdown will just be empty
  }
}

function startEdit(ch) {
  editingChannel.value = ch.channel;
  editForm.value = {
    provider: ch.provider || "",
    auto_join: ch.auto_join,
    topic_prefix: ch.topic_prefix || "",
  };
  saveError.value = "";
}

function cancelEdit() {
  editingChannel.value = null;
  saveError.value = "";
}

async function saveChannel(ch) {
  saving.value = true;
  saveError.value = "";
  try {
    const res = await adminUpdateChannel(ch.channel, {
      provider: editForm.value.provider,
      auto_join: editForm.value.auto_join,
      topic_prefix: editForm.value.topic_prefix,
    });
    if (res.ok) {
      // Update local state
      ch.provider = editForm.value.provider;
      ch.auto_join = editForm.value.auto_join;
      ch.topic_prefix = editForm.value.topic_prefix;
      editingChannel.value = null;
    } else {
      saveError.value = res.error || "Failed to save channel settings";
    }
  } catch {
    saveError.value = "Network error";
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  fetchChannels();
  fetchProviders();
});
</script>
