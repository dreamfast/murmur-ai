<template>
  <div class="p-6">
    <!-- Filter bar -->
    <section class="mb-6">
      <div class="flex flex-wrap items-end gap-3">
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">From</label>
          <input
            v-model="filters.from"
            type="date"
            class="rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          />
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">To</label>
          <input
            v-model="filters.to"
            type="date"
            class="rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          />
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">Channel</label>
          <input
            v-model="filters.channel"
            type="text"
            placeholder="all"
            class="w-32 rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          />
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">Provider</label>
          <input
            v-model="filters.provider"
            type="text"
            placeholder="all"
            class="w-32 rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          />
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">Nick</label>
          <input
            v-model="filters.nick"
            type="text"
            placeholder="all"
            class="w-28 rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          />
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">Type</label>
          <select
            v-model="filters.request_type"
            class="rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          >
            <option value="">all</option>
            <option value="chat">chat</option>
            <option value="task">task</option>
            <option value="event">event</option>
            <option value="summary">summary</option>
          </select>
        </div>
        <div>
          <label class="mb-1 block font-mono text-xs text-text-muted">Period</label>
          <select
            v-model="filters.period"
            class="rounded border border-border bg-bg-input px-3 py-1.5 font-mono text-sm text-text-primary outline-none transition focus:border-border-focus"
          >
            <option value="hour">Hour</option>
            <option value="day">Day</option>
            <option value="week">Week</option>
            <option value="month">Month</option>
          </select>
        </div>
        <button
          class="rounded bg-accent px-4 py-1.5 font-mono text-sm text-white transition hover:bg-accent-hover"
          @click="applyFilters"
        >Apply</button>
      </div>
    </section>

    <!-- Loading -->
    <div v-if="loading && !summary" class="py-8 text-center font-mono text-sm text-text-muted">Loading...</div>

    <!-- Error -->
    <div
      v-else-if="error"
      class="rounded border border-error/30 bg-error/10 px-4 py-3 text-sm text-error"
    >{{ error }}</div>

    <template v-else-if="summary">
      <!-- Summary cards -->
      <section class="mb-8">
        <h2 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
          Overview
        </h2>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <div
            v-for="card in summaryCards"
            :key="card.label"
            class="rounded-lg border border-border bg-bg-secondary p-4"
          >
            <div class="text-xs text-text-muted">{{ card.label }}</div>
            <div class="mt-1 font-mono text-xl font-bold" :class="card.color">{{ card.value }}</div>
            <div v-if="card.sub" class="mt-0.5 text-xs text-text-muted">{{ card.sub }}</div>
          </div>
        </div>
      </section>

      <!-- Charts row -->
      <section class="mb-8 grid gap-6 lg:grid-cols-2">
        <!-- Token usage line chart -->
        <div class="rounded-lg border border-border bg-bg-secondary p-4">
          <h3 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
            Token Usage Over Time
          </h3>
          <div v-if="aggregate.length === 0" class="py-8 text-center text-sm text-text-muted">
            No data for the selected period.
          </div>
          <div v-else class="h-64">
            <canvas ref="tokenChartCanvas"></canvas>
          </div>
        </div>

        <!-- Tool usage bar chart -->
        <div class="rounded-lg border border-border bg-bg-secondary p-4">
          <h3 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
            Tool Usage
          </h3>
          <div v-if="tools.length === 0" class="py-8 text-center text-sm text-text-muted">
            No tool data available.
          </div>
          <div v-else class="h-64">
            <canvas ref="toolChartCanvas"></canvas>
          </div>
        </div>
      </section>

      <!-- Provider breakdown -->
      <section class="mb-8 grid gap-6 lg:grid-cols-2">
        <div class="rounded-lg border border-border bg-bg-secondary p-4">
          <h3 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
            Provider Breakdown
          </h3>
          <div v-if="providers.length === 0" class="py-8 text-center text-sm text-text-muted">
            No provider data available.
          </div>
          <div v-else class="h-64">
            <canvas ref="providerChartCanvas"></canvas>
          </div>
        </div>

        <!-- Provider table -->
        <div class="rounded-lg border border-border bg-bg-secondary p-4">
          <h3 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
            Provider Details
          </h3>
          <div v-if="providers.length === 0" class="py-8 text-center text-sm text-text-muted">
            No provider data available.
          </div>
          <div v-else class="overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead class="border-b border-border">
                <tr>
                  <th class="px-3 py-2 font-mono text-xs font-medium uppercase text-text-muted">Provider</th>
                  <th class="px-3 py-2 font-mono text-xs font-medium uppercase text-text-muted">Requests</th>
                  <th class="px-3 py-2 font-mono text-xs font-medium uppercase text-text-muted">Tokens</th>
                  <th class="px-3 py-2 font-mono text-xs font-medium uppercase text-text-muted">Avg Latency</th>
                  <th class="px-3 py-2 font-mono text-xs font-medium uppercase text-text-muted">Errors</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="p in providers"
                  :key="p.provider"
                  class="border-b border-border last:border-b-0"
                >
                  <td class="px-3 py-2 font-mono text-text-primary">{{ p.provider }}</td>
                  <td class="px-3 py-2 font-mono text-text-secondary">{{ fmtNum(p.total_requests) }}</td>
                  <td class="px-3 py-2 font-mono text-text-secondary">{{ fmtNum(p.total_tokens) }}</td>
                  <td class="px-3 py-2 font-mono text-text-secondary">{{ fmtMs(p.avg_latency_ms) }}</td>
                  <td class="px-3 py-2 font-mono" :class="p.error_count > 0 ? 'text-error' : 'text-text-muted'">
                    {{ p.error_count }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <!-- Raw data table -->
      <section class="mb-6">
        <h2 class="mb-3 font-mono text-sm font-bold uppercase tracking-wider text-text-secondary">
          Recent Requests
          <span class="ml-2 rounded bg-bg-tertiary px-1.5 py-0.5 text-xs text-text-muted">{{ statsTotal }}</span>
        </h2>
        <div class="overflow-x-auto rounded-lg border border-border">
          <table class="w-full text-left text-sm">
            <thead class="border-b border-border bg-bg-tertiary">
              <tr>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Time</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Channel</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Nick</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Provider</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Model</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Tokens</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Tools</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Latency</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Type</th>
                <th class="px-3 py-2 font-mono text-xs font-medium uppercase tracking-wider text-text-muted">Status</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in statsList"
                :key="row.id"
                class="border-b border-border last:border-b-0 hover:bg-bg-hover/50"
              >
                <td class="whitespace-nowrap px-3 py-2 font-mono text-xs text-text-secondary">{{ fmtTime(row.timestamp) }}</td>
                <td class="px-3 py-2 font-mono text-xs text-text-primary">{{ row.channel }}</td>
                <td class="px-3 py-2 font-mono text-xs text-text-primary">{{ row.nick }}</td>
                <td class="px-3 py-2 font-mono text-xs text-text-secondary">{{ row.provider }}</td>
                <td class="max-w-[120px] truncate px-3 py-2 font-mono text-xs text-text-muted" :title="row.model">{{ row.model }}</td>
                <td class="px-3 py-2 font-mono text-xs text-text-secondary">
                  {{ fmtNum(row.total_tokens) }}
                  <span class="text-text-muted">({{ row.prompt_tokens }}+{{ row.completion_tokens }})</span>
                </td>
                <td class="px-3 py-2 font-mono text-xs text-text-secondary">{{ row.tool_calls_count }}</td>
                <td class="px-3 py-2 font-mono text-xs text-text-secondary">{{ fmtMs(row.latency_ms) }}</td>
                <td class="px-3 py-2">
                  <span
                    class="rounded px-1.5 py-0.5 text-xs font-medium"
                    :class="{
                      'bg-accent/20 text-accent': row.request_type === 'chat',
                      'bg-info/20 text-info': row.request_type === 'task',
                      'bg-warning/20 text-warning': row.request_type === 'event',
                      'bg-bg-tertiary text-text-secondary': row.request_type === 'summary',
                    }"
                  >{{ row.request_type }}</span>
                </td>
                <td class="px-3 py-2">
                  <span
                    class="rounded px-1.5 py-0.5 text-xs font-medium"
                    :class="row.status === 'ok' ? 'bg-success/20 text-success' : 'bg-error/20 text-error'"
                    :title="row.error_message || ''"
                  >{{ row.status }}</span>
                </td>
              </tr>
              <tr v-if="statsList.length === 0">
                <td colspan="10" class="px-4 py-8 text-center text-sm text-text-muted">No usage data recorded yet.</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Pagination -->
        <div v-if="statsTotal > pageSize" class="mt-3 flex items-center justify-between">
          <span class="text-xs text-text-muted">
            Showing {{ statsOffset + 1 }}&ndash;{{ Math.min(statsOffset + pageSize, statsTotal) }} of {{ statsTotal }}
          </span>
          <div class="flex gap-1">
            <button
              class="rounded border border-border px-3 py-1 font-mono text-xs text-text-secondary transition hover:bg-bg-hover disabled:opacity-30"
              :disabled="statsOffset === 0"
              @click="changePage(-1)"
            >Prev</button>
            <button
              class="rounded border border-border px-3 py-1 font-mono text-xs text-text-secondary transition hover:bg-bg-hover disabled:opacity-30"
              :disabled="statsOffset + pageSize >= statsTotal"
              @click="changePage(1)"
            >Next</button>
          </div>
        </div>
      </section>

      <!-- Auto-refresh indicator -->
      <div class="flex items-center gap-2">
        <span class="h-1.5 w-1.5 rounded-full bg-success"></span>
        <span class="text-xs text-text-muted">Auto-refreshes every 30s</span>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from "vue";
import {
  adminGetStatsSummary,
  adminGetStatsAggregate,
  adminGetStatsTools,
  adminGetStatsProviders,
  adminGetStatsList,
} from "../../api.js";

const REFRESH_INTERVAL_MS = 30_000;
const pageSize = 25;

// --- Reactive state ---

const loading = ref(false);
const error = ref("");
const summary = ref(null);
const aggregate = ref([]);
const tools = ref([]);
const providers = ref([]);
const statsList = ref([]);
const statsTotal = ref(0);
const statsOffset = ref(0);

const filters = ref({
  from: "",
  to: "",
  channel: "",
  provider: "",
  nick: "",
  request_type: "",
  period: "day",
});

// Chart canvas refs
const tokenChartCanvas = ref(null);
const toolChartCanvas = ref(null);
const providerChartCanvas = ref(null);

// Chart instances (for cleanup)
let tokenChart = null;
let toolChart = null;
let providerChart = null;
let refreshTimer = null;

// Lazy-loaded Chart.js module
let chartModule = null;

// --- Computed ---

const summaryCards = computed(() => {
  const s = summary.value;
  if (!s) return [];
  return [
    {
      label: "Total Requests",
      value: fmtNum(s.total_requests),
      color: "text-text-primary",
      sub: s.top_channel ? `Top: ${s.top_channel}` : null,
    },
    {
      label: "Total Tokens",
      value: fmtNum(s.total_tokens),
      color: "text-accent",
      sub: s.top_provider ? `Top: ${s.top_provider}` : null,
    },
    {
      label: "Tool Calls",
      value: fmtNum(s.total_tool_calls),
      color: "text-info",
    },
    {
      label: "Avg Latency",
      value: fmtMs(s.avg_latency_ms),
      color: "text-warning",
    },
    {
      label: "Errors",
      value: fmtNum(s.error_count),
      color: s.error_count > 0 ? "text-error" : "text-success",
    },
  ];
});

// --- Formatting helpers ---

function fmtNum(n) {
  if (n == null) return "0";
  return Number(n).toLocaleString();
}

function fmtMs(ms) {
  if (ms == null || ms === 0) return "0ms";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function fmtTime(ts) {
  if (!ts) return "\u2014";
  const d = new Date(ts);
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// --- Query builder ---

function buildFilterParams() {
  const p = {};
  if (filters.value.from) p.from = new Date(filters.value.from).toISOString();
  if (filters.value.to) {
    // Set to end of day in UTC for consistent server-side filtering
    const d = new Date(filters.value.to);
    d.setUTCHours(23, 59, 59, 999);
    p.to = d.toISOString();
  }
  if (filters.value.channel) p.channel = filters.value.channel;
  if (filters.value.provider) p.provider = filters.value.provider;
  if (filters.value.nick) p.nick = filters.value.nick;
  if (filters.value.request_type) p.request_type = filters.value.request_type;
  return p;
}

// --- Data fetching ---

async function fetchAll() {
  loading.value = true;
  error.value = "";
  try {
    const params = buildFilterParams();
    const aggParams = { ...params, period: filters.value.period };

    const [summaryRes, aggRes, toolsRes, providersRes, listRes] = await Promise.all([
      adminGetStatsSummary(params),
      adminGetStatsAggregate(aggParams),
      adminGetStatsTools(params),
      adminGetStatsProviders(params),
      adminGetStatsList({ ...params, limit: pageSize, offset: statsOffset.value }),
    ]);

    if (summaryRes.ok) {
      summary.value = summaryRes.data;
    } else {
      error.value = summaryRes.error || "Failed to load stats";
      return;
    }

    aggregate.value = aggRes.ok ? (aggRes.data || []) : [];
    tools.value = toolsRes.ok ? (toolsRes.data || []) : [];
    providers.value = providersRes.ok ? (providersRes.data || []) : [];

    if (listRes.ok && listRes.data) {
      statsList.value = listRes.data.stats || [];
      statsTotal.value = listRes.data.total || 0;
    } else {
      statsList.value = [];
      statsTotal.value = 0;
    }

    // Render charts after DOM updates
    await nextTick();
    await renderCharts();
  } catch {
    error.value = "Network error \u2014 is the server running?";
  } finally {
    loading.value = false;
  }
}

function applyFilters() {
  statsOffset.value = 0;
  fetchAll();
}

function changePage(dir) {
  statsOffset.value = Math.max(0, statsOffset.value + dir * pageSize);
  fetchAll();
}

// --- Chart rendering (lazy-loaded) ---

const CHART_COLORS = {
  accent: "#7c5cbf",
  accentMuted: "rgba(124, 92, 191, 0.3)",
  success: "#4caf50",
  successMuted: "rgba(76, 175, 80, 0.3)",
  info: "#64b5f6",
  infoMuted: "rgba(100, 181, 246, 0.3)",
  warning: "#ff9800",
  warningMuted: "rgba(255, 152, 0, 0.3)",
  error: "#f44336",
  errorMuted: "rgba(244, 67, 54, 0.3)",
  textMuted: "#6b6b80",
  border: "#2e2e50",
};

const PALETTE = [
  "#7c5cbf", "#64b5f6", "#4caf50", "#ff9800", "#f44336",
  "#9370db", "#4dd0e1", "#81c784", "#ffb74d", "#e57373",
];

async function loadChartJS() {
  if (chartModule) return chartModule;
  const mod = await import("chart.js");
  mod.Chart.register(
    mod.CategoryScale,
    mod.LinearScale,
    mod.PointElement,
    mod.LineElement,
    mod.BarElement,
    mod.ArcElement,
    mod.Tooltip,
    mod.Legend,
    mod.Filler,
  );
  chartModule = mod;
  return mod;
}

const CHART_DEFAULTS = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      labels: { color: "#a0a0b8", font: { family: "Hack, monospace", size: 11 } },
    },
    tooltip: {
      backgroundColor: "#1a1a2e",
      titleColor: "#e0e0e0",
      bodyColor: "#a0a0b8",
      borderColor: "#2e2e50",
      borderWidth: 1,
    },
  },
};

const SCALE_DEFAULTS = {
  ticks: { color: "#6b6b80", font: { family: "Hack, monospace", size: 10 } },
  grid: { color: "rgba(46, 46, 80, 0.5)" },
};

async function renderCharts() {
  const { Chart } = await loadChartJS();

  // Token usage line chart — destroy if data is empty
  if (aggregate.value.length === 0 && tokenChart) {
    tokenChart.destroy();
    tokenChart = null;
  }
  if (tokenChartCanvas.value && aggregate.value.length > 0) {
    if (tokenChart) tokenChart.destroy();
    tokenChart = new Chart(tokenChartCanvas.value, {
      type: "line",
      data: {
        labels: aggregate.value.map((a) => a.period),
        datasets: [
          {
            label: "Prompt Tokens",
            data: aggregate.value.map((a) => a.total_prompt_tokens),
            borderColor: CHART_COLORS.accent,
            backgroundColor: CHART_COLORS.accentMuted,
            fill: true,
            tension: 0.3,
          },
          {
            label: "Completion Tokens",
            data: aggregate.value.map((a) => a.total_completion_tokens),
            borderColor: CHART_COLORS.info,
            backgroundColor: CHART_COLORS.infoMuted,
            fill: true,
            tension: 0.3,
          },
        ],
      },
      options: {
        ...CHART_DEFAULTS,
        scales: { x: SCALE_DEFAULTS, y: { ...SCALE_DEFAULTS, beginAtZero: true } },
      },
    });
  }

  // Tool usage bar chart — destroy if data is empty
  if (tools.value.length === 0 && toolChart) {
    toolChart.destroy();
    toolChart = null;
  }
  if (toolChartCanvas.value && tools.value.length > 0) {
    if (toolChart) toolChart.destroy();
    toolChart = new Chart(toolChartCanvas.value, {
      type: "bar",
      data: {
        labels: tools.value.map((t) => t.name),
        datasets: [
          {
            label: "Invocations",
            data: tools.value.map((t) => t.count),
            backgroundColor: tools.value.map((_, i) => PALETTE[i % PALETTE.length]),
            borderRadius: 4,
          },
        ],
      },
      options: {
        ...CHART_DEFAULTS,
        plugins: {
          ...CHART_DEFAULTS.plugins,
          legend: { display: false },
        },
        scales: { x: SCALE_DEFAULTS, y: { ...SCALE_DEFAULTS, beginAtZero: true } },
      },
    });
  }

  // Provider doughnut chart — destroy if data is empty
  if (providers.value.length === 0 && providerChart) {
    providerChart.destroy();
    providerChart = null;
  }
  if (providerChartCanvas.value && providers.value.length > 0) {
    if (providerChart) providerChart.destroy();
    providerChart = new Chart(providerChartCanvas.value, {
      type: "doughnut",
      data: {
        labels: providers.value.map((p) => p.provider),
        datasets: [
          {
            data: providers.value.map((p) => p.total_tokens),
            backgroundColor: providers.value.map((_, i) => PALETTE[i % PALETTE.length]),
            borderColor: "#1a1a2e",
            borderWidth: 2,
          },
        ],
      },
      options: {
        ...CHART_DEFAULTS,
        cutout: "60%",
      },
    });
  }
}

// --- Lifecycle ---

onMounted(() => {
  fetchAll();
  refreshTimer = setInterval(fetchAll, REFRESH_INTERVAL_MS);
});

onUnmounted(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
  if (tokenChart) { tokenChart.destroy(); tokenChart = null; }
  if (toolChart) { toolChart.destroy(); toolChart = null; }
  if (providerChart) { providerChart.destroy(); providerChart = null; }
});
</script>
