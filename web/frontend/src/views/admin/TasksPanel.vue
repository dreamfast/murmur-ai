<template>
  <div class="p-6">
    <!-- Header -->
    <div class="mb-4 flex items-center justify-between">
      <h2 class="font-mono text-lg font-bold text-text-primary">Scheduled Tasks</h2>
      <div class="flex gap-2">
        <button
          class="rounded bg-accent px-3 py-1.5 font-mono text-sm text-white transition hover:bg-accent-hover"
          @click="openCreateModal('cron')"
        >+ Add Task</button>
        <button
          class="rounded border border-accent bg-transparent px-3 py-1.5 font-mono text-sm text-accent transition hover:bg-accent/10"
          @click="openCreateModal('once')"
        >+ Add Reminder</button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="py-8 text-center font-mono text-sm text-text-muted">Loading...</div>

    <!-- Error -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >{{ error }}</div>

    <!-- Tasks table -->
    <div v-else class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full text-left text-sm">
        <thead class="border-b border-border bg-bg-tertiary">
          <tr>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Name</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Schedule</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Channel</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Type</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Enabled</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Last Run</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Next Run</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Created By</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="task in tasks"
            :key="task.id"
            class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
            :class="isOverdue(task) ? 'bg-error/5' : ''"
          >
            <td class="px-4 py-2.5">
              <div class="font-mono text-text-primary">{{ task.name }}</div>
              <div class="mt-0.5 max-w-xs truncate text-xs text-text-muted">{{ task.action }}</div>
            </td>
            <td class="px-4 py-2.5 font-mono text-xs text-text-secondary">
              {{ task.type === 'once' ? formatDateTime(task.run_at) : task.schedule }}
            </td>
            <td class="px-4 py-2.5 font-mono text-xs text-text-secondary">{{ task.channel }}</td>
            <td class="px-4 py-2.5">
              <span
                class="rounded px-2 py-0.5 text-xs font-medium"
                :class="task.type === 'once' ? 'bg-info/20 text-info' : 'bg-accent/20 text-accent'"
              >{{ task.type }}</span>
            </td>
            <td class="px-4 py-2.5">
              <button
                class="relative h-5 w-9 rounded-full transition"
                :class="task.enabled ? 'bg-success' : 'bg-bg-tertiary'"
                @click="toggleTask(task)"
              >
                <span
                  class="absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform"
                  :class="task.enabled ? 'left-[18px]' : 'left-0.5'"
                ></span>
              </button>
            </td>
            <td class="px-4 py-2.5 text-xs text-text-muted">{{ formatDateTime(task.last_run) }}</td>
            <td class="px-4 py-2.5 text-xs" :class="isOverdue(task) ? 'text-error font-medium' : 'text-text-muted'">
              {{ formatDateTime(task.next_run) }}
              <span v-if="isOverdue(task)" class="ml-1">(overdue)</span>
            </td>
            <td class="px-4 py-2.5 font-mono text-xs text-text-muted">{{ task.created_by || '—' }}</td>
            <td class="px-4 py-2.5">
              <button
                class="rounded px-2 py-1 text-xs text-error transition hover:bg-error/10"
                @click="confirmDelete(task)"
              >Delete</button>
            </td>
          </tr>
          <tr v-if="tasks.length === 0">
            <td colspan="9" class="px-4 py-8 text-center text-sm text-text-muted">No scheduled tasks.</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Create Task Modal -->
    <div
      v-if="showModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      @click.self="closeModal"
    >
      <div class="mx-4 w-full max-w-lg rounded-lg border border-border bg-bg-secondary p-6">
        <h3 class="mb-4 font-mono text-base font-bold text-text-primary">
          {{ createType === 'once' ? 'Add Reminder' : 'Add Task' }}
        </h3>

        <div class="space-y-3">
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Name</label>
            <input
              v-model="form.name"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="daily-report"
            />
          </div>

          <div v-if="createType === 'cron'">
            <label class="mb-1 block font-mono text-xs text-text-muted">Schedule (cron expression)</label>
            <input
              v-model="form.schedule"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="0 9 * * *"
            />
            <p class="mt-1 text-xs text-text-muted">e.g. "0 9 * * *" = every day at 9am</p>
          </div>

          <div v-if="createType === 'once'">
            <label class="mb-1 block font-mono text-xs text-text-muted">Run At</label>
            <input
              v-model="form.run_at"
              type="datetime-local"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
            />
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Action (instruction for the AI)</label>
            <textarea
              v-model="form.action"
              rows="3"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="Check the weather and post a summary"
            ></textarea>
          </div>

          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Channel</label>
            <input
              v-model="form.channel"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="#murmur"
            />
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
            @click="createTask"
          >{{ saving ? 'Creating...' : 'Create' }}</button>
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
        <h3 class="mb-2 font-mono text-base font-bold text-text-primary">Delete Task</h3>
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
  adminListTasks,
  adminCreateTask,
  adminDeleteTask,
  adminToggleTask,
} from "../../api.js";

const tasks = ref([]);
const loading = ref(false);
const error = ref("");
const showModal = ref(false);
const createType = ref("cron");
const saving = ref(false);
const modalError = ref("");
const showDeleteConfirm = ref(false);
const deleteTarget = ref(null);

const emptyForm = () => ({
  name: "",
  schedule: "",
  action: "",
  channel: "",
  run_at: "",
});

const form = ref(emptyForm());

async function fetchTasks() {
  loading.value = true;
  error.value = "";
  try {
    const res = await adminListTasks();
    if (res.ok) {
      tasks.value = res.data || [];
    } else {
      error.value = res.error || "Failed to load tasks";
    }
  } catch {
    error.value = "Network error";
  } finally {
    loading.value = false;
  }
}

function formatDateTime(dt) {
  if (!dt) return "—";
  try {
    const d = new Date(dt);
    if (isNaN(d.getTime())) return "—";
    return d.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return "—";
  }
}

function isOverdue(task) {
  if (!task.enabled || !task.next_run) return false;
  try {
    return new Date(task.next_run) < new Date();
  } catch {
    return false;
  }
}

function openCreateModal(type) {
  createType.value = type;
  form.value = emptyForm();
  modalError.value = "";
  showModal.value = true;
}

function closeModal() {
  showModal.value = false;
  modalError.value = "";
}

async function createTask() {
  saving.value = true;
  modalError.value = "";
  try {
    const body = {
      name: form.value.name,
      action: form.value.action,
      channel: form.value.channel,
      type: createType.value,
    };
    if (createType.value === "cron") {
      body.schedule = form.value.schedule;
    } else {
      // Convert local datetime to RFC3339
      if (form.value.run_at) {
        body.run_at = new Date(form.value.run_at).toISOString();
      }
    }
    const res = await adminCreateTask(body);
    if (res.ok) {
      closeModal();
      await fetchTasks();
    } else {
      modalError.value = res.error || "Failed to create task";
    }
  } catch {
    modalError.value = "Network error";
  } finally {
    saving.value = false;
  }
}

async function toggleTask(task) {
  const original = task.enabled;
  task.enabled = !original;
  try {
    const res = await adminToggleTask(task.id, task.enabled);
    if (!res.ok) {
      task.enabled = original;
    }
  } catch {
    task.enabled = original;
  }
}

function confirmDelete(task) {
  deleteTarget.value = task;
  showDeleteConfirm.value = true;
}

async function doDelete() {
  if (!deleteTarget.value) return;
  saving.value = true;
  try {
    const res = await adminDeleteTask(deleteTarget.value.id);
    if (res.ok) {
      showDeleteConfirm.value = false;
      deleteTarget.value = null;
      await fetchTasks();
    } else {
      error.value = res.error || "Failed to delete task";
      showDeleteConfirm.value = false;
    }
  } catch {
    error.value = "Network error";
    showDeleteConfirm.value = false;
  } finally {
    saving.value = false;
  }
}

onMounted(fetchTasks);
</script>
