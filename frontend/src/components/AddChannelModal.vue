<script setup lang="ts">
import { Plus, Trash2, X } from '@lucide/vue';
import { computed, reactive, ref, watch } from 'vue';
import { api, type Upstream, type UpstreamGroupPayload, type UpstreamPayload } from '../services/api';

const props = defineProps<{
  upstream?: Upstream | null;
}>();

const emit = defineEmits<{
  close: [];
  created: [];
  updated: [];
}>();

type BalanceAuthType = 'newapi_access_token' | 'sub2api_refresh_token' | 'password' | 'x_api_key';
type GroupForm = UpstreamGroupPayload;

const submitting = ref(false);
const error = ref('');
const isEditing = computed(() => Boolean(props.upstream));
const showAdvancedOptions = ref(props.upstream?.auth_type === 'x_api_key' || props.upstream?.auth_type === 'admin_api_key');

const form = reactive({
  name: props.upstream?.name ?? '',
  kind: (props.upstream?.kind ?? 'new_api') as 'new_api' | 'sub2api',
  url: props.upstream?.base_url ?? '',
  balance_auth_type: initialBalanceAuthType(props.upstream),
  balance_user_id: '',
  balance_access_token: '',
  balance_refresh_token: '',
  balance_cached_access_token: '',
  auth_username: '',
  auth_password: '',
  api_key: '',
  call_url: '',
  call_key: '',
  enabled: props.upstream?.enabled ?? true,
  poll_interval_seconds: props.upstream?.poll_interval_seconds ?? 1800,
  groups: props.upstream?.groups?.length ? props.upstream.groups.map(normalizeExistingGroup) : [newGroup()],
});

const normalizedUrl = computed(() => normalizeURL(form.url));
const normalizedCallUrl = computed(() => normalizeURL(form.call_url) || normalizedUrl.value);
const hasSavedCallKey = computed(() => Boolean(props.upstream?.auth_secret_masked?.includes('call:')));
const currentBalanceAuthType = computed(() => initialBalanceAuthType(props.upstream));

const balanceModeChanged = computed(() => {
  if (!isEditing.value) return true;
  return currentBalanceAuthType.value !== form.balance_auth_type;
});

const balanceCredentialTouched = computed(() => {
  switch (form.balance_auth_type) {
    case 'newapi_access_token':
      return Boolean(form.balance_user_id.trim() || form.balance_access_token.trim());
    case 'sub2api_refresh_token':
      return Boolean(form.balance_refresh_token.trim() || form.balance_cached_access_token.trim());
    case 'password':
      return Boolean(form.auth_username.trim() || form.auth_password);
    case 'x_api_key':
      return Boolean(form.api_key.trim());
    default:
      return false;
  }
});

const shouldSubmitBalanceCredential = computed(() => !isEditing.value || balanceModeChanged.value || balanceCredentialTouched.value);

const validationMessage = computed(() => {
  if (!normalizedUrl.value) return '请填写后台 URL';

  const enabledGroups = form.groups.filter((group) => group.enabled !== false);
  if (!enabledGroups.length) return '至少保留一个启用的检测分组';
  if (enabledGroups.some((group) => !group.test_model.trim())) return '启用的分组必须填写测试模型';
  if (enabledGroups.some((group) => Number(group.manual_ratio) < 0)) return '分组倍率不能小于 0';

  if (!isEditing.value && !form.call_key.trim()) {
    return '请填写调用密钥 sk-...，用于模型可用性检测';
  }
  if (isEditing.value && !hasSavedCallKey.value && !form.call_key.trim()) {
    return '当前配置还没有调用密钥，请填写 sk-... 后再保存';
  }
  if (form.call_url.trim() && !form.call_key.trim() && isEditing.value) {
    return '编辑调用 URL 时需要同时填写调用密钥';
  }

  if (!shouldSubmitBalanceCredential.value) return '';

  if (form.balance_auth_type === 'newapi_access_token') {
    if (!form.balance_user_id.trim() || !form.balance_access_token.trim()) {
      return 'new-api access_token 模式必须填写用户 ID 和用户 access_token';
    }
  }
  if (form.balance_auth_type === 'sub2api_refresh_token') {
    if (!form.balance_refresh_token.trim()) return 'sub2api refresh_token 模式必须填写 refresh_token';
  }
  if (form.balance_auth_type === 'password') {
    if (!form.auth_username.trim() || !form.auth_password) return '账号密码兼容模式必须填写登录账号和登录密码';
  }
  if (form.balance_auth_type === 'x_api_key') {
    if (!form.api_key.trim()) return '高级 X-API-Key 模式必须填写管理密钥';
  }
  return '';
});

const canSubmit = computed(() => !validationMessage.value);

watch(
  () => form.kind,
  () => {
    if (form.balance_auth_type === 'password' || form.balance_auth_type === 'x_api_key') return;
    form.balance_auth_type = defaultBalanceAuthType(form.kind);
  },
);

function defaultBalanceAuthType(kind: 'new_api' | 'sub2api'): BalanceAuthType {
  return kind === 'new_api' ? 'newapi_access_token' : 'sub2api_refresh_token';
}

function initialBalanceAuthType(upstream?: Upstream | null): BalanceAuthType {
  if (upstream?.auth_type === 'password') return 'password';
  if (upstream?.auth_type === 'x_api_key' || upstream?.auth_type === 'admin_api_key') return 'x_api_key';
  if (upstream?.auth_type === 'newapi_access_token' || upstream?.auth_type === 'new_api_token') return 'newapi_access_token';
  if (upstream?.auth_type === 'sub2api_refresh_token') return 'sub2api_refresh_token';
  return defaultBalanceAuthType((upstream?.kind ?? 'new_api') as 'new_api' | 'sub2api');
}

function newGroup(): GroupForm {
  return {
    name: 'default',
    display_name: 'default',
    test_model: 'gpt-5.5',
    manual_ratio: 1,
    enabled: true,
  };
}

function normalizeURL(value: string) {
  return value.trim().replace(/\/+$/, '');
}

function normalizeExistingGroup(group: UpstreamGroupPayload): GroupForm {
  return {
    id: group.id,
    upstream_id: group.upstream_id,
    name: group.name || 'default',
    display_name: group.display_name || group.name || 'default',
    test_model: group.test_model || 'gpt-5.5',
    manual_ratio: group.manual_ratio ?? 1,
    enabled: group.enabled !== false,
  };
}

function addGroup() {
  form.groups.push({
    ...newGroup(),
    name: `group-${form.groups.length + 1}`,
    display_name: `group-${form.groups.length + 1}`,
  });
}

function removeGroup(index: number) {
  if (form.groups.length === 1) return;
  form.groups.splice(index, 1);
}

function buildGroups() {
  return form.groups
    .map((group, index) => {
      const displayName = group.display_name.trim();
      const name = group.name?.trim() || slugGroupName(displayName, index);
      const ratio = group.manual_ratio === null || group.manual_ratio === undefined || String(group.manual_ratio) === '' ? null : Number(group.manual_ratio);
      return {
        ...group,
        name,
        display_name: displayName || name,
        test_model: group.test_model.trim(),
        manual_ratio: Number.isFinite(ratio) ? ratio : null,
        enabled: Boolean(group.enabled),
      };
    })
    .filter((group) => group.name);
}

function slugGroupName(displayName: string, index: number) {
  const slug = displayName
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9\u4e00-\u9fa5]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug || `group-${index + 1}`;
}

function buildPayload(): UpstreamPayload {
  const base: UpstreamPayload = {
    kind: form.kind,
    url: normalizedUrl.value,
    enabled: form.enabled,
    poll_enabled: form.enabled,
    poll_interval_seconds: Number(form.poll_interval_seconds) || 1800,
    groups: buildGroups(),
  };
  if (form.name.trim()) base.name = form.name.trim();

  if (!isEditing.value || form.call_key.trim()) {
    base.call_url = normalizedCallUrl.value;
    base.call_key = form.call_key.trim();
  }

  if (!shouldSubmitBalanceCredential.value) return base;

  if (form.balance_auth_type === 'newapi_access_token') {
    base.balance_auth_type = 'newapi_access_token';
    base.balance_user_id = form.balance_user_id.trim();
    base.balance_access_token = form.balance_access_token.trim();
  } else if (form.balance_auth_type === 'sub2api_refresh_token') {
    base.balance_auth_type = 'sub2api_refresh_token';
    base.balance_refresh_token = form.balance_refresh_token.trim();
    if (form.balance_cached_access_token.trim()) base.balance_access_token = form.balance_cached_access_token.trim();
  } else if (form.balance_auth_type === 'password') {
    base.balance_auth_type = 'password';
    base.auth_username = form.auth_username.trim();
    base.auth_password = form.auth_password;
  } else if (form.balance_auth_type === 'x_api_key') {
    base.auth_type = 'x_api_key';
    base.api_key = form.api_key.trim();
  }
  return base;
}

async function submit() {
  if (validationMessage.value) {
    error.value = validationMessage.value;
    return;
  }
  submitting.value = true;
  error.value = '';

  try {
    if (props.upstream) {
      await api.updateUpstream(props.upstream.id, buildPayload());
      emit('updated');
    } else {
      await api.createUpstream(buildPayload());
      emit('created');
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : isEditing.value ? '更新失败' : '添加失败';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="modal-backdrop">
    <form class="modal-panel upstream-form" @submit.prevent="submit">
      <header class="modal-header">
        <div>
          <p class="eyebrow">{{ isEditing ? '编辑渠道' : '新增渠道' }}</p>
          <h2>{{ isEditing ? '编辑上游监控源' : '添加上游监控源' }}</h2>
        </div>
        <button class="icon-button ghost" type="button" title="关闭" @click="emit('close')">
          <X :size="18" />
        </button>
      </header>

      <div v-if="error" class="form-error">{{ error }}</div>
      <p v-if="isEditing" class="form-note">敏感字段留空代表不修改已保存凭证。</p>

      <section class="form-section">
        <div class="section-title">
          <span>基础信息</span>
        </div>
        <div class="form-grid">
          <label>
            <span>名称</span>
            <input v-model="form.name" placeholder="留空则按域名生成" autocomplete="off" />
          </label>
          <label>
            <span>类型</span>
            <select v-model="form.kind">
              <option value="new_api">new-api</option>
              <option value="sub2api">sub2api</option>
            </select>
          </label>
          <label class="wide">
            <span>后台 URL</span>
            <input v-model="form.url" placeholder="https://new-api.example.com" autocomplete="off" />
          </label>
        </div>
      </section>

      <section class="form-section">
        <div class="section-title">
          <span>余额查询凭证</span>
          <button class="text-button" type="button" @click="showAdvancedOptions = !showAdvancedOptions">
            {{ showAdvancedOptions ? '隐藏高级' : '高级模式' }}
          </button>
        </div>

        <div class="form-grid">
          <label class="wide">
            <span>余额认证方式</span>
            <select v-model="form.balance_auth_type">
              <option v-if="form.kind === 'new_api'" value="newapi_access_token">推荐：new-api 用户 access_token</option>
              <option v-if="form.kind === 'sub2api'" value="sub2api_refresh_token">推荐：sub2api refresh_token</option>
              <option value="password">兼容：账号密码登录</option>
              <option v-if="showAdvancedOptions" value="x_api_key">高级：管理员 X-API-Key</option>
            </select>
            <small class="field-hint">余额 token 只用于查额度，不是 sk 调用密钥。</small>
          </label>

          <template v-if="form.balance_auth_type === 'newapi_access_token'">
            <label>
              <span>用户 ID</span>
              <input v-model="form.balance_user_id" :placeholder="isEditing ? '留空则不修改凭证' : '例如 3222'" autocomplete="off" />
            </label>
            <label>
              <span>用户 access_token</span>
              <input
                v-model="form.balance_access_token"
                type="password"
                :placeholder="isEditing ? '留空则不修改凭证' : '用于查额度，不是 sk 调用密钥'"
                autocomplete="new-password"
              />
            </label>
            <details class="help-panel wide">
              <summary>如何获取 new-api access_token</summary>
              <ol>
                <li>登录 new-api 后台。</li>
                <li>在控制台执行下面的命令。</li>
                <li>返回 JSON 中的 data 填入 access_token。</li>
                <li>用户 ID 来自 /api/user/self 或页面用户信息。</li>
              </ol>
              <pre><code>fetch('/api/user/token', {
  credentials: 'include',
  headers: { 'New-Api-User': '你的用户ID' }
}).then(r => r.json()).then(console.log)</code></pre>
              <p>new-api 的 /api/user/token 返回 data 后一般长期有效。</p>
            </details>
          </template>

          <template v-if="form.balance_auth_type === 'sub2api_refresh_token'">
            <label class="wide">
              <span>refresh_token</span>
              <input
                v-model="form.balance_refresh_token"
                type="password"
                :placeholder="isEditing ? '留空则不修改凭证' : '从 sub2api Local Storage 复制 refresh_token'"
                autocomplete="new-password"
              />
            </label>
            <label class="wide">
              <span>access_token，可选</span>
              <input
                v-model="form.balance_cached_access_token"
                type="password"
                :placeholder="isEditing ? '留空则不修改凭证' : '可留空，后端会通过 refresh_token 换取'"
                autocomplete="new-password"
              />
            </label>
            <details class="help-panel wide">
              <summary>如何获取 sub2api refresh_token</summary>
              <ol>
                <li>登录 sub2api 后台。</li>
                <li>打开 F12 -> Application -> Local Storage -> 对应域名。</li>
                <li>复制 refresh_token。</li>
                <li>后端会自动续期，不需要每天抓 auth_token。</li>
              </ol>
              <p>sub2api 的 refresh_token 默认约 30 天，后端会自动续期并保存新的 refresh_token。</p>
            </details>
          </template>

          <template v-if="form.balance_auth_type === 'password'">
            <label>
              <span>登录账号</span>
              <input v-model="form.auth_username" :placeholder="isEditing ? '留空则不修改凭证' : 'admin'" autocomplete="username" />
            </label>
            <label>
              <span>登录密码</span>
              <input
                v-model="form.auth_password"
                type="password"
                :placeholder="isEditing ? '留空则不修改凭证' : '上游后台登录密码'"
                autocomplete="new-password"
              />
            </label>
            <p class="form-note wide">有验证码、Turnstile 或 Cloudflare 拦截的上游可能无法自动登录，不建议依赖账号密码作为主流程。</p>
          </template>

          <template v-if="form.balance_auth_type === 'x_api_key'">
            <label class="wide">
              <span>管理员 X-API-Key</span>
              <input v-model="form.api_key" type="password" :placeholder="isEditing ? '留空则不修改凭证' : '仅用于兼容特殊部署'" autocomplete="new-password" />
            </label>
          </template>
        </div>
      </section>

      <section class="form-section">
        <div class="section-title">
          <span>可用性检测</span>
          <button class="text-button" type="button" @click="addGroup">
            <Plus :size="16" />
            添加分组
          </button>
        </div>

        <div class="form-grid availability-grid">
          <label>
            <span>调用 URL</span>
            <input v-model="form.call_url" placeholder="留空默认后台 URL" autocomplete="off" />
            <small class="field-hint">留空时使用后台 URL。</small>
          </label>
          <label>
            <span>调用密钥 sk-...</span>
            <input
              v-model="form.call_key"
              type="password"
              :placeholder="isEditing ? '留空则不修改调用密钥' : 'sk-...，用于模型可用性检测'"
              autocomplete="new-password"
            />
            <small class="field-hint">call_key/sk 只用于检测模型是否能调用，不一定能查余额。</small>
          </label>
        </div>

        <div class="groups-editor">
          <div v-for="(group, index) in form.groups" :key="index" class="group-row">
            <label>
              <span>分组显示名</span>
              <input v-model="group.display_name" placeholder="default" autocomplete="off" />
            </label>
            <label>
              <span>测试模型</span>
              <input v-model="group.test_model" placeholder="gpt-5.5" autocomplete="off" />
            </label>
            <label>
              <span>分组倍率</span>
              <input v-model.number="group.manual_ratio" min="0" step="0.0001" type="number" placeholder="1" />
            </label>
            <label class="toggle-line group-enabled">
              <input v-model="group.enabled" type="checkbox" />
              <span>启用</span>
            </label>
            <button class="icon-button danger" type="button" title="删除分组" :disabled="form.groups.length === 1" @click="removeGroup(index)">
              <Trash2 :size="17" />
            </button>
          </div>
        </div>
      </section>

      <footer class="modal-actions">
        <label class="toggle-line inline-toggle">
          <input v-model="form.enabled" type="checkbox" />
          <span>启用轮询</span>
        </label>
        <label class="interval-field">
          <span>轮询间隔</span>
          <input v-model.number="form.poll_interval_seconds" min="60" step="60" type="number" />
        </label>
        <button class="secondary-button" type="button" @click="emit('close')">取消</button>
        <button class="primary-button" type="submit" :disabled="submitting || !canSubmit" :title="validationMessage || undefined">
          {{ submitting ? '保存中...' : isEditing ? '保存修改' : '保存并刷新' }}
        </button>
      </footer>
    </form>
  </div>
</template>
