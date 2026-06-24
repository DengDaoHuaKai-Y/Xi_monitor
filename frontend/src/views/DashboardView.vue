<script setup lang="ts">
import { LogOut, Pencil, Plus, RefreshCw, ShieldCheck, Trash2 } from '@lucide/vue';
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import AddChannelModal from '../components/AddChannelModal.vue';
import SparkLine, { type TrendSample } from '../components/SparkLine.vue';
import { api, type DashboardData, type MonitorItem, type Upstream } from '../services/api';
import { clearToken } from '../services/auth';

const router = useRouter();
const TREND_HISTORY_LIMIT = 60;
const dashboard = ref<DashboardData>({
  summary: {
    total: 0,
    available: 0,
    unavailable: 0,
    avg_latency_ms: null,
  },
  items: [],
});
const selectedWindow = ref('6h');
const loading = ref(true);
const refreshing = ref(false);
const error = ref('');
const showModal = ref(false);
const metric = ref('first_token');
const deletingUpstreamId = ref<number | null>(null);
const editingUpstream = ref<Upstream | null>(null);
const loadingEditUpstreamId = ref<number | null>(null);
const updatingRatioKey = ref('');
const refreshingRows = ref<Set<number>>(new Set());
const upstreams = ref<Upstream[]>([]);
let pollTimer: number | undefined;
let refreshPollTimers: number[] = [];
let refreshPollRunId = 0;

const windows = [
  { label: '6h', hours: 6 },
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
];

const sortedItems = computed(() => {
  const items = Array.isArray(dashboard.value.items) ? dashboard.value.items : [];
  return [...items].sort((a, b) => (b.latency_ms ?? 0) - (a.latency_ms ?? 0));
});

const availabilityText = computed(() => {
  const summary = dashboard.value.summary;
  if (!summary.total) return '0%';
  return `${Math.round((summary.available / summary.total) * 100)}%`;
});

function statusLabel(status: string) {
  const map: Record<string, string> = {
    available: '可用',
    unavailable: '不可用',
    error: '异常',
    unknown: '未知',
  };
  return map[status] || status;
}

function availabilityStatus(item: MonitorItem) {
  return item.availability_status || item.status || 'unknown';
}

function availabilityStatusLabel(item: MonitorItem) {
  const status = availabilityStatus(item);
  const map: Record<string, string> = {
    available: '可用',
    unavailable: '不可用',
    error: '不可用',
    unknown: '未检测',
  };
  return map[status] || statusLabel(status);
}

function balanceStatus(item: MonitorItem) {
  if (item.balance_status) {
    if (item.balance_status === 'ok') return 'normal';
    return item.balance_status;
  }
  const message = sanitizedMessage(item).toLowerCase();
  if (/credential invalid|凭证.*(过期|失效)|token.*(expired|invalid)|refresh.*failed|401|403/.test(message)) return 'expired';
  if (item.balance !== undefined && item.balance !== null && item.balance !== '') return 'normal';
  if (!item.last_checked_at && item.status === 'unknown') return 'unknown';
  if (item.status === 'error' || message) return 'failed';
  return 'unknown';
}

function balanceStatusLabel(item: MonitorItem) {
  const map: Record<string, string> = {
    normal: '正常',
    expired: '凭证过期',
    failed: '查询失败',
    error: '查询失败',
    unknown: '未检测',
  };
  const status = balanceStatus(item);
  return map[status] || status;
}

function sourceLabel(source: string) {
  if (!source) return '未知';
  return source.replace('new_api', 'new-api').replace('new-api', 'new-api').replace('sub2api', 'sub2api');
}

function formatDate(value?: string) {
  if (!value) return '-';
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(value));
}

function formatNumber(value?: string | number | null, fallback = '-') {
  if (value === undefined || value === null || value === '') return fallback;
  const number = Number(value);
  if (!Number.isFinite(number)) return String(value);
  return number.toLocaleString('zh-CN', { maximumFractionDigits: 4 });
}

function formatRatio(value?: string | number | null) {
  return formatNumber(value);
}

function lastBalanceCheckedAt(item: MonitorItem) {
  return item.last_balance_checked_at || item.last_checked_at;
}

function lastAvailabilityCheckedAt(item: MonitorItem) {
  return item.last_availability_checked_at || item.last_checked_at;
}

function checkedTimeValue(value?: string) {
  if (!value) return 0;
  const time = new Date(value).getTime();
  return Number.isFinite(time) ? time : 0;
}

function latestCheckedAtForUpstream(upstreamId: number) {
  const items = dashboard.value.items ?? [];
  return items
    .filter((item) => item.upstream_id === upstreamId)
    .reduce((latest, item) => Math.max(latest, checkedTimeValue(lastAvailabilityCheckedAt(item)), checkedTimeValue(item.last_checked_at)), 0);
}

function captureRefreshBaselines(upstreamIds: Iterable<number>) {
  const baselines = new Map<number, number>();
  for (const upstreamId of upstreamIds) {
    baselines.set(upstreamId, latestCheckedAtForUpstream(upstreamId));
  }
  return baselines;
}

function sanitizedMessage(item: MonitorItem) {
  return sanitizeSensitiveText(item.last_error || item.last_message || '');
}

function sanitizeSensitiveText(value: string) {
  return value
    .replace(/Bearer\s+[A-Za-z0-9._~+\-/=]+/gi, 'Bearer ***')
    .replace(/sk-[A-Za-z0-9][A-Za-z0-9_-]{8,}/g, 'sk-***')
    .replace(/((?:access|refresh|auth)?_?token|api_?key|call_?key|password)(["'\s:=]+)([^"',\s}]+)/gi, '$1$2***');
}

function ratioKey(item: MonitorItem) {
  return `${item.upstream_id}:${item.group_name || 'default'}`;
}

function findItemGroup(item: MonitorItem) {
  const upstream = upstreams.value.find((candidate) => candidate.id === item.upstream_id);
  return upstream?.groups?.find((group) => {
    if (item.upstream_group_id && group.id === item.upstream_group_id) return true;
    return group.name === item.group_name || group.display_name === item.group_name;
  });
}

function groupDisplayName(item: MonitorItem) {
  if (!item.group_name) return '-';
  const group = findItemGroup(item);
  return group?.display_name || group?.name || item.group_name;
}

function internalGroupName(item: MonitorItem) {
  const group = findItemGroup(item);
  return group?.name || item.group_name || 'default';
}

function endpointHost(item: MonitorItem) {
  try {
    return new URL(item.endpoint).host;
  } catch {
    return item.endpoint || item.source || '-';
  }
}

function historyFor(item: MonitorItem) {
  const rawTrend = typeof item.trend === 'string' ? parseTrendString(item.trend) : item.trend;
  const samples = (Array.isArray(rawTrend) ? rawTrend : [])
    .map((sample, index) => normalizeTrendSample(sample, item, index))
    .filter((sample): sample is TrendSample => Boolean(sample))
    .slice(-TREND_HISTORY_LIMIT);
  const windowDef = windows.find((window) => window.label === selectedWindow.value) ?? windows[0];
  const cutoff = Date.now() - windowDef.hours * 60 * 60 * 1000;
  return samples.filter((sample) => {
    const time = new Date(sample.checkedAt).getTime();
    return Number.isFinite(time) && time >= cutoff;
  });
}

function parseTrendString(value: string) {
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function normalizeTrendSample(sample: number | { value?: number; status?: string; checked_at?: string; checkedAt?: string }, item: MonitorItem, index: number) {
  if (typeof sample === 'number') {
    return {
      value: Math.max(1, Number(sample) || 1),
      status: item.status || 'unknown',
      checkedAt: item.last_checked_at || new Date(Date.now() - (TREND_HISTORY_LIMIT - index) * 60000).toISOString(),
    };
  }
  const checkedAt = sample.checked_at || sample.checkedAt || item.last_checked_at;
  if (!checkedAt) return null;
  return {
    value: Math.max(1, Number(sample.value) || 1),
    status: sample.status || item.status || 'unknown',
    checkedAt,
  };
}

function setRowRefreshing(upstreamId: number, value: boolean) {
  const next = new Set(refreshingRows.value);
  if (value) next.add(upstreamId);
  else next.delete(upstreamId);
  refreshingRows.value = next;
}

function isRowRefreshing(item: MonitorItem) {
  return refreshingRows.value.has(item.upstream_id);
}

function updateDisplayedRatio(target: MonitorItem, ratio: number | null) {
  const value = ratio === null ? undefined : String(ratio);
  dashboard.value = {
    ...dashboard.value,
    items: (dashboard.value.items ?? []).map((item) => {
      if (item.upstream_id !== target.upstream_id) return item;
      if (item.upstream_group_id && target.upstream_group_id && item.upstream_group_id !== target.upstream_group_id) return item;
      if (!item.upstream_group_id && !target.upstream_group_id && item.group_name !== target.group_name) return item;
      return { ...item, ratio: value };
    }),
  };
}

function clearRefreshPollTimers() {
  for (const timer of refreshPollTimers) window.clearTimeout(timer);
  refreshPollTimers = [];
}

function scheduleRefreshPolling(upstreamIds: number[], baselines: Map<number, number>) {
  clearRefreshPollTimers();
  const ids = [...new Set(upstreamIds)].filter((id) => Number.isFinite(id));
  if (!ids.length) {
    refreshing.value = false;
    refreshingRows.value = new Set();
    return;
  }

  const runId = ++refreshPollRunId;
  let attempts = 0;
  const maxAttempts = 28;

  const poll = async () => {
    if (runId !== refreshPollRunId) return;
    attempts += 1;
    await loadDashboard(true);
    if (runId !== refreshPollRunId) return;

    const pending = ids.filter((id) => latestCheckedAtForUpstream(id) <= (baselines.get(id) ?? 0));
    refreshingRows.value = new Set(pending);

    if (!pending.length || attempts >= maxAttempts) {
      refreshing.value = false;
      if (attempts >= maxAttempts) refreshingRows.value = new Set();
      return;
    }

    const delay = attempts < 8 ? 900 : 1800;
    const timer = window.setTimeout(poll, delay);
    refreshPollTimers.push(timer);
  };

  const timer = window.setTimeout(poll, 650);
  refreshPollTimers.push(timer);
}

async function loadDashboard(silent = false) {
  if (!silent) loading.value = true;
  error.value = '';
  try {
    const [nextDashboard, nextUpstreams] = await Promise.all([
      api.dashboard(),
      api.listUpstreams().catch(() => upstreams.value),
    ]);
    dashboard.value = nextDashboard;
    upstreams.value = nextUpstreams;
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载失败';
    dashboard.value = {
      summary: {
        total: 0,
        available: 0,
        unavailable: 0,
        avg_latency_ms: null,
      },
      items: [],
    };
  } finally {
    loading.value = false;
  }
}

async function refreshAll() {
  refreshing.value = true;
  error.value = '';
  try {
    const ids = new Set([...sortedItems.value.map((item) => item.upstream_id), ...upstreams.value.map((upstream) => upstream.id)]);
    const baselines = captureRefreshBaselines(ids);
    await api.refreshDashboard();
    refreshingRows.value = ids;
    scheduleRefreshPolling([...ids], baselines);
  } catch (err) {
    error.value = err instanceof Error ? err.message : '刷新失败';
    refreshingRows.value = new Set();
    refreshing.value = false;
  } finally {
  }
}

async function refreshRow(item: MonitorItem) {
  setRowRefreshing(item.upstream_id, true);
  error.value = '';
  try {
    const baselines = captureRefreshBaselines([item.upstream_id]);
    if (item.id > 0) {
      await api.refreshItem(item.id);
    } else {
      await api.refreshUpstream(item.upstream_id);
    }
    scheduleRefreshPolling([item.upstream_id], baselines);
  } catch (err) {
    error.value = err instanceof Error ? err.message : '刷新失败';
    setRowRefreshing(item.upstream_id, false);
  }
}

async function deleteRow(item: MonitorItem) {
  const confirmed = window.confirm(`确定删除「${item.name}」所属的上游渠道吗？相关分组、监控项和检测日志会一起删除。`);
  if (!confirmed) return;

  deletingUpstreamId.value = item.upstream_id;
  error.value = '';
  try {
    await api.deleteUpstream(item.upstream_id);
    await loadDashboard(true);
  } catch (err) {
    error.value = err instanceof Error ? err.message : '删除失败';
  } finally {
    deletingUpstreamId.value = null;
  }
}

async function editRow(item: MonitorItem) {
  loadingEditUpstreamId.value = item.upstream_id;
  error.value = '';
  try {
    const nextUpstreams = await api.listUpstreams();
    upstreams.value = nextUpstreams;
    const upstream = nextUpstreams.find((candidate) => candidate.id === item.upstream_id);
    if (!upstream) {
      throw new Error('未找到对应上游配置');
    }
    editingUpstream.value = upstream;
    showModal.value = true;
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载编辑配置失败';
  } finally {
    loadingEditUpstreamId.value = null;
  }
}

async function resolveRatioGroupName(item: MonitorItem) {
  let groupName = internalGroupName(item);
  if (groupName !== item.group_name || upstreams.value.length) return groupName;

  upstreams.value = await api.listUpstreams();
  groupName = internalGroupName(item);
  return groupName;
}

async function editRatio(item: MonitorItem) {
  const value = window.prompt('设置分组倍率，留空则清除手动倍率', item.ratio ? String(Number(item.ratio)) : '');
  if (value === null) return;

  const trimmed = value.trim();
  const ratio = trimmed === '' ? null : Number(trimmed);
  if (ratio !== null && (!Number.isFinite(ratio) || ratio <= 0)) {
    error.value = '倍率必须是大于 0 的数字';
    return;
  }

  const key = ratioKey(item);
  updatingRatioKey.value = key;
  error.value = '';
  try {
    const groupName = await resolveRatioGroupName(item);
    await api.setGroupRatio(item.upstream_id, groupName, ratio);
    updateDisplayedRatio(item, ratio);
    await loadDashboard(true);
    setRowRefreshing(item.upstream_id, true);
    const baselines = captureRefreshBaselines([item.upstream_id]);
    await api.refreshUpstream(item.upstream_id);
    scheduleRefreshPolling([item.upstream_id], baselines);
  } catch (err) {
    error.value = err instanceof Error ? err.message : '设置倍率失败';
    setRowRefreshing(item.upstream_id, false);
  } finally {
    updatingRatioKey.value = '';
  }
}

async function handleCreated() {
  showModal.value = false;
  editingUpstream.value = null;
  await loadDashboard(true);
  window.setTimeout(() => loadDashboard(true), 1200);
  window.setTimeout(() => loadDashboard(true), 3200);
}

async function handleUpdated() {
  showModal.value = false;
  editingUpstream.value = null;
  await loadDashboard(true);
}

function closeModal() {
  showModal.value = false;
  editingUpstream.value = null;
}

function logout() {
  clearToken();
  router.replace('/login');
}

function onAuthExpired() {
  router.replace('/login');
}

onMounted(() => {
  loadDashboard();
  pollTimer = window.setInterval(() => loadDashboard(true), 30000);
  window.addEventListener('auth-expired', onAuthExpired);
});

onBeforeUnmount(() => {
  if (pollTimer) window.clearInterval(pollTimer);
  clearRefreshPollTimers();
  window.removeEventListener('auth-expired', onAuthExpired);
});
</script>

<template>
  <main class="dashboard-shell">
    <header class="app-header">
      <div class="brand">
        <div class="brand-mark">X</div>
        <div>
          <h1>Xi Monitor</h1>
          <p>API 渠道可用性与延迟监控</p>
        </div>
      </div>
      <button class="icon-button ghost" title="退出登录" @click="logout">
        <LogOut :size="18" />
      </button>
    </header>

    <section class="dashboard-panel">
      <div class="toolbar">
        <div class="time-filter">
          <span>时间维度</span>
          <div class="segmented">
            <button v-for="window in windows" :key="window.label" :class="{ active: selectedWindow === window.label }" @click="selectedWindow = window.label">
              {{ window.label }}
            </button>
          </div>
        </div>

        <div class="toolbar-actions">
          <div class="monitor-pill">
            <ShieldCheck :size="16" />
            监测中
          </div>
          <span class="timestamp">{{ formatDate(dashboard.summary.last_refreshed_at) }}</span>
          <button class="icon-button add" title="新增渠道" @click="showModal = true">
            <Plus :size="20" />
          </button>
          <button class="outline-button" :disabled="refreshing" @click="refreshAll">
            <RefreshCw :class="{ spinning: refreshing }" :size="16" />
            刷新
          </button>
        </div>
      </div>

      <div class="summary-strip">
        <div>
          <span>总渠道</span>
          <strong>{{ dashboard.summary.total }}</strong>
        </div>
        <div>
          <span>可用</span>
          <strong>{{ dashboard.summary.available }}</strong>
        </div>
        <div>
          <span>异常</span>
          <strong>{{ dashboard.summary.unavailable }}</strong>
        </div>
        <div>
          <span>平均延迟</span>
          <strong>{{ dashboard.summary.avg_latency_ms ? Math.round(dashboard.summary.avg_latency_ms) : '-' }} ms</strong>
        </div>
        <div>
          <span>可用率</span>
          <strong>{{ availabilityText }}</strong>
        </div>
      </div>

      <p class="notice">首Token 可能因技术限制与中转站后台不一致，可横向对比各站响应。</p>
      <p v-if="error" class="form-error dashboard-error">{{ error }}</p>

      <div class="table-wrap">
        <table class="monitor-table">
          <thead>
            <tr>
              <th class="api-col">API</th>
              <th>分组</th>
              <th>倍率</th>
              <th>可用性</th>
              <th class="latency-col">首Token ↓</th>
              <th>可用率</th>
              <th class="balance-col">余额</th>
              <th>
                <select v-model="metric" class="metric-select" aria-label="曲线指标">
                  <option value="first_token">首Token</option>
                  <option value="latency">总延迟</option>
                </select>
              </th>
              <th class="checked-col">最近监测</th>
              <th class="action-col">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="10" class="empty-cell">正在加载监控数据...</td>
            </tr>
            <tr v-else-if="!sortedItems.length">
              <td colspan="10" class="empty-cell">暂无渠道数据，点击右上角加号新增渠道。</td>
            </tr>
            <tr v-for="item in sortedItems" v-else :key="`${item.upstream_id}-${item.item_type}-${item.id || item.external_id}`">
              <td class="api-cell" data-label="API">
                <div class="api-name">
                  <strong>{{ item.name }}</strong>
                  <span :class="['source-tag', item.source?.includes('sub2api') ? 'sub2api' : 'newapi']">{{ sourceLabel(item.source) }}</span>
                </div>
                <small>{{ endpointHost(item) }}</small>
              </td>
              <td data-label="分组">{{ groupDisplayName(item) }}</td>
              <td data-label="倍率">
                <div class="ratio-cell">
                  <span>{{ formatRatio(item.ratio) }}</span>
                  <button
                    class="mini-icon-button"
                    type="button"
                    title="设置手动倍率"
                    :disabled="updatingRatioKey === ratioKey(item)"
                    @click="editRatio(item)"
                  >
                    <Pencil :size="13" />
                  </button>
                </div>
              </td>
              <td data-label="可用性">
                <div class="status-stack">
                  <span :class="['status-badge', availabilityStatus(item)]">{{ availabilityStatusLabel(item) }}</span>
                  <small>检测 {{ formatDate(lastAvailabilityCheckedAt(item)) }}</small>
                </div>
              </td>
              <td class="latency-cell" data-label="首Token">
                <strong class="latency">{{ item.latency_ms ?? '-' }} ms</strong>
                <small v-if="sanitizedMessage(item)">{{ sanitizedMessage(item) }}</small>
              </td>
              <td data-label="可用率">{{ formatNumber(item.availability_percent) }}%</td>
              <td data-label="余额">
                <div class="balance-detail">
                  <span :class="['mini-status', balanceStatus(item)]">{{ balanceStatusLabel(item) }}</span>
                  <span>
                    {{ formatNumber(item.balance) }}
                    <small v-if="item.balance_unit">{{ item.balance_unit }}</small>
                  </span>
                  <small>查询 {{ formatDate(lastBalanceCheckedAt(item)) }}</small>
                </div>
              </td>
              <td class="trend-cell" data-label="趋势"><SparkLine :samples="historyFor(item)" :refreshing="isRowRefreshing(item)" /></td>
              <td data-label="最近监测">{{ formatDate(item.last_checked_at) }}</td>
              <td data-label="操作">
                <div class="row-actions">
                  <button class="icon-button ghost" title="刷新此渠道" :disabled="isRowRefreshing(item)" @click="refreshRow(item)">
                    <RefreshCw :class="{ spinning: isRowRefreshing(item) }" :size="16" />
                  </button>
                  <button
                    class="icon-button ghost"
                    title="编辑此上游渠道"
                    :disabled="loadingEditUpstreamId === item.upstream_id"
                    @click="editRow(item)"
                  >
                    <Pencil :size="16" />
                  </button>
                  <button
                    class="icon-button danger"
                    title="删除此上游渠道"
                    :disabled="deletingUpstreamId === item.upstream_id"
                    @click="deleteRow(item)"
                  >
                    <Trash2 :size="16" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <AddChannelModal v-if="showModal" :upstream="editingUpstream" @close="closeModal" @created="handleCreated" @updated="handleUpdated" />
  </main>
</template>
