<template>
  <div class="my-1 rounded-lg border bg-bg-secondary" :class="borderClass">
    <!-- Header (always visible) -->
    <button
      class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm"
      @click="expanded = !expanded"
    >
      <span class="text-base">{{ icon }}</span>
      <span class="font-mono font-bold" :class="textClass">{{ approval.name }}</span>
      <span class="truncate text-xs text-text-muted">({{ approval.args || "..." }})</span>
      <span class="ml-auto flex items-center gap-2">
        <span v-if="status" class="rounded px-1.5 py-0.5 text-xs font-medium" :class="statusClass">
          {{ status }}
        </span>
        <span class="text-xs text-text-muted">{{ expanded ? "▲" : "▼" }}</span>
      </span>
    </button>

    <!-- Expanded content -->
    <div v-if="expanded" class="border-t border-border px-3 py-2">
      <div class="mb-2 font-mono text-xs text-text-muted">
        ID: {{ approval.id }}
      </div>
      <div v-if="approval.args" class="mb-2 rounded bg-bg-tertiary p-2 font-mono text-xs text-text-secondary">
        {{ approval.args }}
      </div>

      <!-- Approve/Deny buttons (only when pending) -->
      <div v-if="!status" class="flex gap-2">
        <button
          class="rounded bg-success/20 px-3 py-1.5 text-xs font-medium text-success transition hover:bg-success/30"
          @click="$emit('approve', approval.id)"
        >
          &#x2705; Approve
        </button>
        <button
          class="rounded bg-error/20 px-3 py-1.5 text-xs font-medium text-error transition hover:bg-error/30"
          @click="$emit('deny', approval.id)"
        >
          &#x274C; Deny
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from "vue";

const props = defineProps({
  /** The parsed approval request object. */
  approval: {
    type: Object,
    required: true,
  },
  /** Status: null (pending), "approved", "denied", "timeout". */
  status: {
    type: String,
    default: null,
  },
});

defineEmits(["approve", "deny"]);

const expanded = ref(!props.status); // Auto-expand pending items.

const icon = computed(() => {
  switch (props.status) {
    case "approved": return "\u2705";
    case "denied": return "\u274C";
    case "timeout": return "\u23F0";
    default: return "\u26A0\uFE0F";
  }
});

const borderClass = computed(() => {
  switch (props.status) {
    case "approved": return "border-success/30";
    case "denied": return "border-error/30";
    case "timeout": return "border-warning/30";
    default: return "border-warning/50";
  }
});

const textClass = computed(() => {
  switch (props.status) {
    case "approved": return "text-success";
    case "denied": return "text-error";
    case "timeout": return "text-warning";
    default: return "text-warning";
  }
});

const statusClass = computed(() => {
  switch (props.status) {
    case "approved": return "bg-success/20 text-success";
    case "denied": return "bg-error/20 text-error";
    case "timeout": return "bg-warning/20 text-warning";
    default: return "";
  }
});
</script>
