import { clearToken, getToken } from './auth';

export type ApiEnvelope<T> = {
  success: boolean;
  message: string;
  data: T;
};

export type DashboardSummary = {
  total: number;
  available: number;
  unavailable: number;
  avg_latency_ms: number | null;
  last_refreshed_at?: string;
};

export type MonitorItem = {
  id: number;
  upstream_id: number;
  upstream_group_id?: number;
  external_id: string;
  item_type: string;
  name: string;
  endpoint: string;
  source: string;
  group_name: string;
  ratio?: string;
  status: 'unknown' | 'available' | 'unavailable' | 'error' | string;
  balance_status?: 'ok' | 'normal' | 'expired' | 'failed' | 'unknown' | string;
  availability_status?: 'available' | 'unavailable' | 'unknown' | 'error' | string;
  latency_ms?: number | null;
  availability_percent?: string;
  balance?: string;
  balance_unit?: string;
  last_checked_at?: string;
  last_balance_checked_at?: string;
  last_availability_checked_at?: string;
  last_message?: string;
  last_error?: string;
  trend?: Array<number | { value?: number; status?: string; checked_at?: string; checkedAt?: string }> | string | null;
};

export type DashboardData = {
  summary: DashboardSummary;
  items: MonitorItem[] | null;
};

const emptySummary: DashboardSummary = {
  total: 0,
  available: 0,
  unavailable: 0,
  avg_latency_ms: null,
};

export type UpstreamGroupPayload = {
  id?: number;
  upstream_id?: number;
  name: string;
  display_name: string;
  manual_ratio?: number | null;
  test_model: string;
  enabled: boolean;
};

export type UpstreamPayload = {
  name?: string;
  kind: 'new_api' | 'sub2api';
  url: string;
  base_url?: string;
  auth_type?: 'password' | 'new_api_token' | 'x_api_key' | 'bearer' | 'cookie' | 'admin_api_key';
  balance_auth_type?: 'newapi_access_token' | 'sub2api_refresh_token' | 'password';
  balance_user_id?: string;
  balance_access_token?: string;
  balance_refresh_token?: string;
  auth_secret?: string;
  auth_username?: string;
  auth_password?: string;
  call_url?: string;
  call_key?: string;
  access_token?: string;
  user_id?: string;
  api_key?: string;
  key?: string;
  enabled?: boolean;
  poll_enabled?: boolean;
  poll_interval_seconds?: number;
  groups?: UpstreamGroupPayload[];
};

export type Upstream = {
  id: number;
  name: string;
  kind: 'new_api' | 'sub2api';
  base_url: string;
  auth_type:
    | 'password'
    | 'newapi_access_token'
    | 'sub2api_refresh_token'
    | 'new_api_token'
    | 'new_api_session'
    | 'x_api_key'
    | 'bearer'
    | 'cookie'
    | 'admin_api_key';
  auth_secret_masked: string;
  enabled: boolean;
  poll_interval_seconds: number;
  last_polled_at?: string;
  last_error?: string;
  balance_status?: 'ok' | 'normal' | 'expired' | 'failed' | 'unknown' | string;
  availability_status?: 'available' | 'unavailable' | 'unknown' | 'error' | string;
  last_balance_checked_at?: string;
  last_availability_checked_at?: string;
  groups?: UpstreamGroupPayload[];
};

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set('Content-Type', 'application/json');

  const token = getToken();
  if (token) headers.set('Authorization', `Bearer ${token}`);

  const response = await fetch(path, { ...options, headers });
  const body = (await response.json().catch(() => null)) as ApiEnvelope<T> | null;

  if (response.status === 401) {
    clearToken();
    window.dispatchEvent(new CustomEvent('auth-expired'));
  }

  if (!response.ok || !body?.success) {
    throw new Error(body?.message || `请求失败 (${response.status})`);
  }

  return body.data;
}

export const api = {
  login(username: string, password: string) {
    return request<{ token: string }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    });
  },
  me() {
    return request<{ username: string }>('/api/auth/me');
  },
  dashboard() {
    return request<DashboardData>('/api/dashboard').then((data) => ({
      summary: { ...emptySummary, ...(data?.summary ?? {}) },
      items: Array.isArray(data?.items) ? data.items : [],
    }));
  },
  refreshDashboard() {
    return request<{ running: boolean; message?: string }>('/api/dashboard/refresh', { method: 'POST' });
  },
  createUpstream(payload: UpstreamPayload) {
    return request('/api/upstreams', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  listUpstreams() {
    return request<Upstream[]>('/api/upstreams').then((data) => (Array.isArray(data) ? data : []));
  },
  updateUpstream(id: number, payload: UpstreamPayload) {
    return request(`/api/upstreams/${id}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    });
  },
  refreshItem(id: number) {
    return request(`/api/items/${id}/refresh`, { method: 'POST' });
  },
  refreshUpstream(id: number) {
    return request(`/api/upstreams/${id}/refresh`, { method: 'POST' });
  },
  setGroupRatio(upstreamId: number, groupName: string, ratio: number | null) {
    return request<UpstreamGroupPayload>(`/api/upstreams/${upstreamId}/groups/${encodeURIComponent(groupName)}/ratio`, {
      method: 'PUT',
      body: JSON.stringify({ ratio }),
    });
  },
  deleteUpstream(id: number) {
    return request<{ deleted: boolean }>(`/api/upstreams/${id}`, { method: 'DELETE' });
  },
};
