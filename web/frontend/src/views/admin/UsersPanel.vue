<template>
  <div class="p-6">
    <!-- Header -->
    <div class="mb-4 flex items-center justify-between">
      <h2 class="font-mono text-lg font-bold text-text-primary">Users</h2>
      <button
        class="rounded bg-accent px-3 py-1.5 font-mono text-sm text-white transition hover:bg-accent-hover"
        @click="openCreateModal"
      >+ Add User</button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="py-8 text-center font-mono text-sm text-text-muted">Loading...</div>

    <!-- Error -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >{{ error }}</div>

    <!-- Users table -->
    <div v-else class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full text-left text-sm">
        <thead class="border-b border-border bg-bg-tertiary">
          <tr>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Nick</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Role</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Autonomy</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Tools</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Rate Limit</th>
            <th class="px-4 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="user in users"
            :key="user.nick"
            class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
          >
            <td class="px-4 py-2.5 font-mono text-text-primary">{{ user.nick }}</td>
            <td class="px-4 py-2.5">
              <span
                class="rounded px-2 py-0.5 text-xs font-medium"
                :class="user.role === 'admin' ? 'bg-accent/20 text-accent' : 'bg-bg-tertiary text-text-secondary'"
              >{{ user.role }}</span>
            </td>
            <td class="px-4 py-2.5">
              <span
                class="rounded px-2 py-0.5 text-xs font-medium"
                :class="{
                  'bg-success/20 text-success': user.autonomy === 'auto',
                  'bg-warning/20 text-warning': user.autonomy === 'approve',
                  'bg-info/20 text-info': user.autonomy === 'report',
                  'bg-bg-tertiary text-text-muted': !user.autonomy,
                }"
              >{{ user.autonomy || 'default' }}</span>
            </td>
            <td class="px-4 py-2.5 font-mono text-xs text-text-secondary">
              {{ formatToolsList(user.tools) }}
            </td>
            <td class="px-4 py-2.5 font-mono text-xs text-text-secondary">
              {{ user.max_messages_per_hour || 'unlimited' }}
            </td>
            <td class="px-4 py-2.5">
              <div class="flex gap-1">
                <button
                  class="rounded px-2 py-1 text-xs text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
                  @click="openEditModal(user)"
                >Edit</button>
                <button
                  class="rounded px-2 py-1 text-xs text-error transition hover:bg-error/10"
                  @click="confirmDelete(user)"
                >Delete</button>
              </div>
            </td>
          </tr>
          <tr v-if="users.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-sm text-text-muted">No users configured.</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Modal overlay -->
    <div
      v-if="showModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      @click.self="closeModal"
    >
      <div class="mx-4 w-full max-w-lg rounded-lg border border-border bg-bg-secondary p-6">
        <h3 class="mb-4 font-mono text-base font-bold text-text-primary">
          {{ isEditing ? 'Edit User' : 'Add User' }}
        </h3>

        <div class="space-y-3">
          <!-- Nick -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Nick</label>
            <input
              v-model="form.nick"
              :disabled="isEditing"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus disabled:opacity-50"
              placeholder="username"
            />
          </div>

          <!-- Role -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Role</label>
            <select
              v-model="form.role"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
            >
              <option value="user">user</option>
              <option value="admin">admin</option>
            </select>
          </div>

          <!-- Autonomy -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Autonomy</label>
            <select
              v-model="form.autonomy"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
            >
              <option value="">default</option>
              <option value="report">report</option>
              <option value="approve">approve</option>
              <option value="auto">auto</option>
            </select>
          </div>

          <!-- Tools (tag input) -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Allowed Tools</label>
            <div class="flex flex-wrap gap-1 rounded border border-border bg-bg-input p-2">
              <span
                v-for="(tool, i) in form.tools"
                :key="i"
                class="flex items-center gap-1 rounded bg-bg-tertiary px-2 py-0.5 font-mono text-xs text-text-secondary"
              >
                {{ tool }}
                <button class="text-text-muted hover:text-error" @click="form.tools.splice(i, 1)">&times;</button>
              </span>
              <input
                v-model="toolInput"
                type="text"
                class="min-w-[80px] flex-1 bg-transparent font-mono text-xs text-text-primary outline-none"
                placeholder="type and press Enter"
                @keydown.enter.prevent="addTag('tools')"
              />
            </div>
          </div>

          <!-- Deny Tools (tag input) -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Denied Tools</label>
            <div class="flex flex-wrap gap-1 rounded border border-border bg-bg-input p-2">
              <span
                v-for="(tool, i) in form.deny_tools"
                :key="i"
                class="flex items-center gap-1 rounded bg-error/20 px-2 py-0.5 font-mono text-xs text-error"
              >
                {{ tool }}
                <button class="text-error/60 hover:text-error" @click="form.deny_tools.splice(i, 1)">&times;</button>
              </span>
              <input
                v-model="denyToolInput"
                type="text"
                class="min-w-[80px] flex-1 bg-transparent font-mono text-xs text-text-primary outline-none"
                placeholder="type and press Enter"
                @keydown.enter.prevent="addTag('deny_tools')"
              />
            </div>
          </div>

          <!-- Rate Limit -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">Max Messages/Hour (0 = unlimited)</label>
            <input
              v-model.number="form.max_messages_per_hour"
              type="number"
              min="0"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
            />
          </div>

          <!-- NickServ Account -->
          <div>
            <label class="mb-1 block font-mono text-xs text-text-muted">NickServ Account</label>
            <input
              v-model="form.nickserv_account"
              type="text"
              class="w-full rounded border border-border bg-bg-input px-3 py-2 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
              placeholder="optional"
            />
          </div>
        </div>

        <!-- Modal error -->
        <div
          v-if="modalError"
          class="mt-3 rounded border border-error/30 bg-error/10 px-3 py-2 text-xs text-error"
        >{{ modalError }}</div>

        <!-- Modal actions -->
        <div class="mt-4 flex justify-end gap-2">
          <button
            class="rounded px-3 py-1.5 text-sm text-text-secondary transition hover:bg-bg-hover hover:text-text-primary"
            @click="closeModal"
          >Cancel</button>
          <button
            class="rounded bg-accent px-3 py-1.5 text-sm text-white transition hover:bg-accent-hover disabled:opacity-50"
            :disabled="saving"
            @click="saveUser"
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
        <h3 class="mb-2 font-mono text-base font-bold text-text-primary">Delete User</h3>
        <p class="mb-4 text-sm text-text-secondary">
          Are you sure you want to delete <span class="font-mono font-bold text-text-primary">{{ deleteTarget?.nick }}</span>?
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
  adminListUsers,
  adminCreateUser,
  adminUpdateUser,
  adminDeleteUser,
} from "../../api.js";

const users = ref([]);
const loading = ref(false);
const error = ref("");
const showModal = ref(false);
const isEditing = ref(false);
const saving = ref(false);
const modalError = ref("");
const toolInput = ref("");
const denyToolInput = ref("");
const showDeleteConfirm = ref(false);
const deleteTarget = ref(null);

const emptyForm = () => ({
  nick: "",
  role: "user",
  autonomy: "",
  tools: ["*"],
  deny_tools: [],
  max_messages_per_hour: 0,
  nickserv_account: "",
});

const form = ref(emptyForm());

async function fetchUsers() {
  loading.value = true;
  error.value = "";
  try {
    const res = await adminListUsers();
    if (res.ok) {
      users.value = res.data || [];
    } else {
      error.value = res.error || "Failed to load users";
    }
  } catch {
    error.value = "Network error";
  } finally {
    loading.value = false;
  }
}

function formatToolsList(tools) {
  if (!tools || tools.length === 0) return "none";
  if (tools.length === 1 && tools[0] === "*") return "all (*)";
  if (tools.length <= 3) return tools.join(", ");
  return `${tools.slice(0, 2).join(", ")} +${tools.length - 2}`;
}

function openCreateModal() {
  form.value = emptyForm();
  isEditing.value = false;
  modalError.value = "";
  toolInput.value = "";
  denyToolInput.value = "";
  showModal.value = true;
}

function openEditModal(user) {
  form.value = {
    nick: user.nick,
    role: user.role,
    autonomy: user.autonomy || "",
    tools: [...(user.tools || [])],
    deny_tools: [...(user.deny_tools || [])],
    max_messages_per_hour: user.max_messages_per_hour || 0,
    nickserv_account: user.nickserv_account || "",
  };
  isEditing.value = true;
  modalError.value = "";
  toolInput.value = "";
  denyToolInput.value = "";
  showModal.value = true;
}

function closeModal() {
  showModal.value = false;
  modalError.value = "";
}

function addTag(field) {
  const input = field === "tools" ? toolInput : denyToolInput;
  const val = input.value.trim();
  if (val && !form.value[field].includes(val)) {
    form.value[field].push(val);
  }
  input.value = "";
}

async function saveUser() {
  saving.value = true;
  modalError.value = "";
  try {
    let res;
    if (isEditing.value) {
      res = await adminUpdateUser(form.value.nick, {
        role: form.value.role,
        tools: form.value.tools,
        deny_tools: form.value.deny_tools,
        autonomy: form.value.autonomy,
        max_messages_per_hour: form.value.max_messages_per_hour,
        nickserv_account: form.value.nickserv_account,
      });
    } else {
      res = await adminCreateUser(form.value);
    }
    if (res.ok) {
      closeModal();
      await fetchUsers();
    } else {
      modalError.value = res.error || "Failed to save user";
    }
  } catch {
    modalError.value = "Network error";
  } finally {
    saving.value = false;
  }
}

function confirmDelete(user) {
  deleteTarget.value = user;
  showDeleteConfirm.value = true;
}

async function doDelete() {
  if (!deleteTarget.value) return;
  saving.value = true;
  try {
    const res = await adminDeleteUser(deleteTarget.value.nick);
    if (res.ok) {
      showDeleteConfirm.value = false;
      deleteTarget.value = null;
      await fetchUsers();
    } else {
      error.value = res.error || "Failed to delete user";
      showDeleteConfirm.value = false;
    }
  } catch {
    error.value = "Network error";
    showDeleteConfirm.value = false;
  } finally {
    saving.value = false;
  }
}

onMounted(fetchUsers);
</script>
