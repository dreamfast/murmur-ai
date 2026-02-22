<template>
  <div class="p-6">
    <!-- Available Tools (read-only) -->
    <section class="mb-8">
      <div class="mb-3 flex items-center justify-between">
        <h2 class="font-mono text-lg font-bold text-text-primary">Available Tools</h2>
        <span class="rounded bg-bg-tertiary px-2 py-0.5 font-mono text-xs text-text-muted">
          {{ allTools.length }} total
        </span>
      </div>

      <div v-if="loadingAll" class="py-4 text-center font-mono text-sm text-text-muted">Loading...</div>
      <div
        v-else-if="errorAll"
        class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
      >{{ errorAll }}</div>

      <div v-else class="overflow-x-auto rounded-lg border border-border">
        <table class="w-full text-left text-sm">
          <thead class="border-b border-border bg-bg-tertiary">
            <tr>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Name</th>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Description</th>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Source</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="tool in allTools"
              :key="tool.name"
              class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
            >
              <td class="px-4 py-2.5 font-mono text-text-primary">{{ tool.name }}</td>
              <td class="max-w-md truncate px-4 py-2.5 text-text-secondary">{{ tool.description }}</td>
              <td class="px-4 py-2.5">
                <span
                  class="rounded px-2 py-0.5 text-xs font-medium"
                  :class="{
                    'bg-accent/20 text-accent': tool.source === 'server',
                    'bg-info/20 text-info': tool.source === 'client',
                    'bg-warning/20 text-warning': tool.source === 'custom',
                  }"
                >{{ tool.source }}</span>
              </td>
            </tr>
            <tr v-if="allTools.length === 0">
              <td colspan="3" class="px-4 py-8 text-center text-sm text-text-muted">No tools available.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Custom Tools (editable) -->
    <section>
      <div class="mb-3 flex items-center justify-between">
        <h2 class="font-mono text-lg font-bold text-text-primary">Custom Tools</h2>
        <button
          class="rounded bg-accent px-3 py-1.5 font-mono text-sm text-white transition hover:bg-accent-hover"
          @click="openCreateModal"
        >+ Add Custom Tool</button>
      </div>

      <div v-if="loadingCustom" class="py-4 text-center font-mono text-sm text-text-muted">Loading...</div>
      <div
        v-else-if="errorCustom"
        class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
      >{{ errorCustom }}</div>

      <div v-else class="overflow-x-auto rounded-lg border border-border">
        <table class="w-full text-left text-sm">
          <thead class="border-b border-border bg-bg-tertiary">
            <tr>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Name</th>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Backend</th>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Enabled</th>
              <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="tool in customTools"
              :key="tool.name"
              class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
            >
              <td class="px-4 py-2.5">
                <div class="font-mono text-text-primary">{{ tool.name }}</div>
                <div class="mt-0.5 max-w-sm truncate text-xs text-text-muted">{{ tool.description }}</div>
              </td>
              <td class="px-4 py-2.5 font-mono text-xs text-text-secondary">{{ tool.backend }}</td>
              <td class="px-4 py-2.5">
                <button
                  class="relative h-5 w-9 rounded-full transition"
                  :class="tool.enabled ? 'bg-success' : 'bg-bg-tertiary'"
                  @click="toggleTool(tool)"
                >
                  <span
                    class="absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform"
                    :class="tool.enabled ? 'left-[18px]' : 'left-0.5'"
                  ></span>
                </button>
              </td>
              <td class="px-4 py-2.5">
                <div class="flex gap-1">
                  <button
                    class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
                    @click="openEditModal(tool)"
                  >Edit</button>
                  <button
                    class="rounded px-2 py-1 text-xs text-error transition hover:bg-error/10"
                    @click="confirmDelete(tool)"
                  >Delete</button>
                </div>
              </td>
            </tr>
            <tr v-if="customTools.length === 0">
              <td colspan="4" class="px-4 py-8 text-center text-sm text-text-muted">No custom tools defined.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Custom Tool Modal -->
    <div
      v-if="showModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      @click.self="closeModal"
    >
      <div class="mx-4 w-full max-w-lg rounded-lg border border-border bg-bg-secondary p-6">
        <h3 class="mb-4 font-mono text-base font-bold text-text-primary">
          {{ isEditing ? 'Edit Custom Tool' : 'Add Custom Tool' }}
        </h3>

        <div class="space-y-3">
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Name</label>
            <input
              v-model="form.name"
              :disabled="isEditing"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus disabled:opacity-50"
              placeholder="my_tool"
            />
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Description</label>
            <input
              v-model="form.description"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="What this tool does"
            />
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Backend</label>
            <input
              v-model="form.backend"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="http, shell, etc."
            />
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Parameters (JSON)</label>
            <textarea
              v-model="form.parameters"
              rows="3"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-xs text-text-primary outline-none transition focus:border-border-focus"
              placeholder="{&quot;type&quot;:&quot;object&quot;,&quot;properties&quot;:{}}"
            ></textarea>
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Backend Config (JSON)</label>
            <textarea
              v-model="form.backend_config"
              rows="3"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-xs text-text-primary outline-none transition focus:border-border-focus"
              placeholder="{&quot;url&quot;:&quot;https://...&quot;}"
            ></textarea>
          </div>

          <div class="flex items-center gap-2">
            <input
              v-model="form.enabled"
              type="checkbox"
              class="h-4 w-4 rounded border-border bg-bg-input accent-accent"
            />
            <label class="font-mono text-xs text-text-muted">Enabled</label>
          </div>
        </div>

        <div
          v-if="modalError"
          class="mt-3 rounded border border-error/30 bg-error/10 px-3 py-2 text-xs text-error"
        >{{ modalError }}</div>

        <div class="mt-4 flex justify-end gap-2">
          <button
            class="rounded px-3 py-1.5 text-sm text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
            @click="closeModal"
          >Cancel</button>
          <button
            class="rounded bg-accent px-3 py-1.5 text-sm text-white transition hover:bg-accent-hover disabled:opacity-50"
            :disabled="saving"
            @click="saveTool"
          >{{ saving ? 'Saving...' : (isEditing ? 'Update' : 'Create') }}</button>
        </div>
      </div>
    </div>

    <!-- Delete confirmation -->
    <div
      v-if="showDeleteConfirm"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      @click.self="showDeleteConfirm = false"
    >
      <div class="mx-4 w-full max-w-sm rounded-lg border border-border bg-bg-secondary p-6">
        <h3 class="mb-2 font-mono text-base font-bold text-text-primary">Delete Custom Tool</h3>
        <p class="mb-4 text-sm text-text-secondary">
          Are you sure you want to delete <span class="font-mono font-bold text-text-primary">{{ deleteTarget?.name }}</span>?
        </p>
        <div class="flex justify-end gap-2">
          <button
            class="rounded px-3 py-1.5 text-sm text-text-secondary transition hover:bg-bg-hover"
            @click="showDeleteConfirm = false"
          >Cancel</button>
          <button
            class="rounded bg-error px-3 py-1.5 text-sm text-white transition hover:bg-error/80 disabled:opacity-50"
            :disabled="saving"
            @click="doDelete"
          >{{ saving ? 'Deleting...' : 'Delete' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from "vue";
import {
  adminListTools,
  adminListCustomTools,
  adminCreateCustomTool,
  adminUpdateCustomTool,
  adminDeleteCustomTool,
  adminToggleCustomTool,
} from "../../api.js";

const allTools = ref([]);
const customTools = ref([]);
const loadingAll = ref(false);
const loadingCustom = ref(false);
const errorAll = ref("");
const errorCustom = ref("");
const showModal = ref(false);
const isEditing = ref(false);
const saving = ref(false);
const modalError = ref("");
const showDeleteConfirm = ref(false);
const deleteTarget = ref(null);

const emptyForm = () => ({
  name: "",
  description: "",
  backend: "",
  parameters: "",
  backend_config: "",
  enabled: true,
});

const form = ref(emptyForm());

async function fetchAllTools() {
  loadingAll.value = true;
  errorAll.value = "";
  try {
    const res = await adminListTools();
    if (res.ok) {
      allTools.value = res.data || [];
    } else {
      errorAll.value = res.error || "Failed to load tools";
    }
  } catch {
    errorAll.value = "Network error";
  } finally {
    loadingAll.value = false;
  }
}

async function fetchCustomTools() {
  loadingCustom.value = true;
  errorCustom.value = "";
  try {
    const res = await adminListCustomTools();
    if (res.ok) {
      customTools.value = res.data || [];
    } else {
      errorCustom.value = res.error || "Failed to load custom tools";
    }
  } catch {
    errorCustom.value = "Network error";
  } finally {
    loadingCustom.value = false;
  }
}

function openCreateModal() {
  form.value = emptyForm();
  isEditing.value = false;
  modalError.value = "";
  showModal.value = true;
}

function openEditModal(tool) {
  form.value = {
    name: tool.name,
    description: tool.description || "",
    backend: tool.backend || "",
    parameters: tool.parameters || "",
    backend_config: tool.backend_config || "",
    enabled: tool.enabled,
  };
  isEditing.value = true;
  modalError.value = "";
  showModal.value = true;
}

function closeModal() {
  showModal.value = false;
  modalError.value = "";
}

async function saveTool() {
  saving.value = true;
  modalError.value = "";
  try {
    let res;
    if (isEditing.value) {
      res = await adminUpdateCustomTool(form.value.name, {
        description: form.value.description,
        backend: form.value.backend,
        parameters: form.value.parameters,
        backend_config: form.value.backend_config,
        enabled: form.value.enabled,
      });
    } else {
      res = await adminCreateCustomTool(form.value);
    }
    if (res.ok) {
      closeModal();
      await Promise.all([fetchAllTools(), fetchCustomTools()]);
    } else {
      modalError.value = res.error || "Failed to save tool";
    }
  } catch {
    modalError.value = "Network error";
  } finally {
    saving.value = false;
  }
}

async function toggleTool(tool) {
  const original = tool.enabled;
  tool.enabled = !original;
  try {
    const res = await adminToggleCustomTool(tool.name, tool.enabled);
    if (!res.ok) {
      tool.enabled = original;
    }
  } catch {
    tool.enabled = original;
  }
}

function confirmDelete(tool) {
  deleteTarget.value = tool;
  showDeleteConfirm.value = true;
}

async function doDelete() {
  if (!deleteTarget.value) return;
  saving.value = true;
  try {
    const res = await adminDeleteCustomTool(deleteTarget.value.name);
    if (res.ok) {
      showDeleteConfirm.value = false;
      deleteTarget.value = null;
      await Promise.all([fetchAllTools(), fetchCustomTools()]);
    } else {
      errorCustom.value = res.error || "Failed to delete tool";
      showDeleteConfirm.value = false;
    }
  } catch {
    errorCustom.value = "Network error";
    showDeleteConfirm.value = false;
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  fetchAllTools();
  fetchCustomTools();
});
</script>
