<template>
  <div class="flex flex-1 flex-col overflow-hidden">
    <!-- Tab navigation -->
    <nav class="flex shrink-0 border-b border-border bg-bg-secondary px-4">
      <router-link
        v-for="tab in tabs"
        :key="tab.name"
        :to="{ name: tab.name }"
        class="relative flex items-center gap-1.5 px-3 py-3 font-mono text-sm transition"
        :class="
          $route.name === tab.name
            ? 'text-accent'
            : 'text-text-muted hover:text-text-secondary'
        "
      >
        <span>{{ tab.icon }}</span>
        <span>{{ tab.label }}</span>
        <span
          v-if="$route.name === tab.name"
          class="absolute inset-x-0 bottom-0 h-0.5 bg-accent"
        ></span>
      </router-link>
    </nav>

    <!-- Tab content -->
    <div class="flex-1 overflow-y-auto">
      <router-view />
    </div>
  </div>
</template>

<script setup>
const tabs = [
  { name: "admin-users", label: "Users", icon: "\u{1F465}" },
  { name: "admin-tools", label: "Tools", icon: "\u{1F527}" },
  { name: "admin-tasks", label: "Tasks", icon: "\u{23F0}" },
  { name: "admin-channels", label: "Channels", icon: "\u{1F4E2}" },
  { name: "admin-stats", label: "Stats", icon: "\u{1F4CA}" },
  { name: "admin-system", label: "System", icon: "\u{2699}" },
];
</script>
