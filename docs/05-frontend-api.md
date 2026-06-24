# 前端 API 对接文档

本文档以当前后端实现为准，供 Vue 前端对接使用。

## 基础约定

- 默认服务地址：`http://localhost:8080`
- API 前缀：`/api`
- 请求体：`Content-Type: application/json`
- 除登录外，所有接口都需要请求头：

```http
Authorization: Bearer <token>
```

## 统一响应格式

成功：

```json
{
  "success": true,
  "message": "",
  "data": {}
}
```

失败：

```json
{
  "success": false,
  "message": "error message"
}
```

常见状态码：

- `200`：成功
- `400`：请求参数错误
- `401`：未登录或 token 无效
- `404`：资源不存在
- `502`：上游测试或刷新失败
- `500`：服务端错误

## 数据枚举

### 上游类型 `kind`

- `new_api`
- `sub2api`

### 认证类型 `auth_type`

- `newapi_access_token`
- `sub2api_refresh_token`
- `new_api_token`
- `new_api_session`，后端运行时临时使用，前端不需要提交
- `x_api_key`
- `password`
- `bearer`
- `cookie`
- `admin_api_key`

### 监控状态 `status`

- `unknown`
- `available`
- `unavailable`
- `error`

## 认证接口

### 登录

`POST /api/auth/login`

请求：

```json
{
  "username": "admin",
  "password": "your-password"
}
```

响应 `data`：

```json
{
  "token": "jwt-token"
}
```

前端拿到 token 后放入后续请求的 `Authorization` 头。

### 当前用户

`GET /api/auth/me`

响应 `data`：

```json
{
  "username": "admin"
}
```

### 退出

`POST /api/auth/logout`

当前后端不维护服务端 session，前端删除本地 token 即可。

响应 `data`：

```json
{}
```

## 仪表盘接口

### 获取仪表盘数据

`GET /api/dashboard`

响应 `data`：

```json
{
  "summary": {
    "total": 1,
    "available": 1,
    "unavailable": 0,
    "avg_latency_ms": 123,
    "last_refreshed_at": "2026-06-23T20:33:07.123456+08:00"
  },
  "items": [
    {
      "id": 1,
      "upstream_id": 1,
      "upstream_group_id": 1,
      "external_id": "channel-1",
      "item_type": "channel",
      "name": "Test Channel",
      "endpoint": "https://example.com",
      "source": "new-api",
      "group_name": "default",
      "ratio": "0.120000",
      "status": "available",
      "latency_ms": 123,
      "availability_percent": "100.00",
      "balance": "12.50000000",
      "balance_unit": "USD",
      "last_checked_at": "2026-06-23T20:33:07.123456+08:00",
      "last_message": "ok",
      "trend": [123],
      "raw_summary": {
        "id": "channel-1"
      },
      "created_at": "2026-06-23T20:33:07.123456+08:00",
      "updated_at": "2026-06-23T20:33:07.123456+08:00"
    }
  ]
}
```

字段说明：

- `summary.total`：监控项总数
- `summary.available`：可用项数量
- `summary.unavailable`：`unavailable` 或 `error` 数量
- `summary.avg_latency_ms`：平均延迟，可能为 `null`
- `items[].ratio`、`availability_percent`、`balance`：后端以字符串返回，避免小数精度问题
- `items[].trend`：简单曲线数据，当前通常是最近一次延迟或状态值
- `items[].raw_summary`：脱敏后的上游摘要

### 刷新全部

`POST /api/dashboard/refresh`

行为：

- 立即触发后台刷新全部启用的上游。
- 如果已有全局刷新任务在跑，会返回当前任务状态。
- 接口返回时刷新可能仍在后台执行。

响应 `data`：

```json
{
  "running": true,
  "started_at": "2026-06-23T20:33:07.123456+08:00",
  "finished_at": "0001-01-01T00:00:00Z",
  "message": "refreshing"
}
```

## 上游配置接口

### 添加上游

`POST /api/upstreams`

请求：

主推荐格式：余额查询使用长期凭证，可用性检测只使用调用 URL + 调用密钥。

new-api：

```json
{
  "name": "我的 new-api",
  "kind": "new_api",
  "url": "https://new-api.example.com",
  "balance_auth_type": "newapi_access_token",
  "balance_user_id": "1",
  "balance_access_token": "new-api-user-access-token",
  "call_url": "https://new-api.example.com",
  "call_key": "sk-your-call-key",
  "groups": [
    {
      "name": "default",
      "display_name": "默认分组",
      "test_model": "gpt-4o-mini",
      "manual_ratio": 0.25,
      "enabled": true
    }
  ]
}
```

sub2api：

```json
{
  "name": "我的 sub2api",
  "kind": "sub2api",
  "url": "https://sub2api.example.com",
  "balance_auth_type": "sub2api_refresh_token",
  "balance_refresh_token": "sub2api-refresh-token",
  "balance_access_token": "optional-cached-access-token",
  "call_url": "https://sub2api.example.com",
  "call_key": "sk-your-call-key"
}
```

兼容格式：账号密码。仅用于没有验证码、Turnstile 或 Cloudflare 拦截的旧部署。

```json
{
  "name": "我的 new-api",
  "kind": "new_api",
  "url": "https://new-api.example.com",
  "balance_auth_type": "password",
  "auth_username": "admin",
  "auth_password": "your-password",
  "call_url": "https://new-api.example.com",
  "call_key": "sk-your-call-key"
}
```

兼容格式：旧管理凭证。保留给已有配置和特殊部署，不作为推荐主流程。

```json
{
  "name": "我的 sub2api",
  "kind": "sub2api",
  "url": "https://sub2api.example.com",
  "auth_type": "x_api_key",
  "api_key": "sub2api-admin-x-api-key"
}
```

必填字段：

- `kind`
- `url`，兼容旧字段 `base_url`
- new-api 主流程传 `balance_auth_type = newapi_access_token`、`balance_access_token`、`balance_user_id`
- sub2api 主流程传 `balance_auth_type = sub2api_refresh_token`、`balance_refresh_token`
- 可用性验证传 `call_url` 和 `call_key`，两者都传时后端会调用 OpenAI 兼容接口 `POST /v1/chat/completions` 发送真实流式请求验证调用密钥是否可用
- 可用性验证只需要前端提供 `groups[].test_model`；后端固定使用提示词 `hi`、端点 `chat_completions`、流式模式
- `call_url` 不传时默认使用 `url`
- `call_key` 兼容旧字段 `key`
- `balance_access_token` 可兼容旧字段 `access_token`
- `balance_user_id` 可兼容旧字段 `user_id`
- `password` 兼容模式传 `auth_username` 和 `auth_password`

默认值：

- `name` 不传时，会从 `url` 的 host 推导
- `enabled` 默认为 `true`
- `poll_interval_seconds` 默认为 `1800`
- `groups` 不传时，后端自动创建一个 `default` 分组
- `groups[].enabled` 默认为 `true`
- `groups[].test_model` 不传时后端会使用默认模型

响应 `data`：

```json
{
  "id": 1,
  "name": "我的 new-api",
  "kind": "new_api",
  "base_url": "https://new-api.example.com",
  "auth_type": "newapi_access_token",
  "auth_secret_masked": "newapi_access_token user:1 ************oken call:************-key",
  "enabled": true,
  "poll_interval_seconds": 1800,
  "created_at": "2026-06-23T20:33:07.123456+08:00",
  "updated_at": "2026-06-23T20:33:07.123456+08:00",
  "groups": [
    {
      "id": 1,
      "upstream_id": 1,
      "name": "default",
      "display_name": "默认分组",
      "manual_ratio": 0.25,
      "test_model": "gpt-4o-mini",
      "enabled": true,
      "created_at": "2026-06-23T20:33:07.123456+08:00",
      "updated_at": "2026-06-23T20:33:07.123456+08:00"
    }
  ]
}
```

说明：

- 余额查询和可用性检测完全分离。余额查询只使用 `balance_*` 凭证；可用性检测只使用 `call_url + call_key`。
- 所有 token、key、password、refresh_token 都会 AES-GCM 加密保存。
- 响应不会返回明文凭证，只返回 `auth_secret_masked`，日志和错误消息也会脱敏。
- 后端不会读取或保存上游里的明文 channel/account 密钥。
- 添加成功后，后端会在后台立即触发一次该上游刷新。
- new-api access token 失效时，只会把余额凭证标记为失败；不会影响 `call_key` 可用性检测。
- sub2api refresh token 会自动刷新 access token，并保存新的 `balance_access_token`、`balance_refresh_token` 和 `balance_token_expires_at`。

### 获取上游列表

`GET /api/upstreams`

响应 `data`：

```json
[
  {
    "id": 1,
    "name": "我的 new-api",
    "kind": "new_api",
    "base_url": "https://new-api.example.com",
    "auth_type": "password",
    "auth_secret_masked": "admin",
    "enabled": true,
    "poll_interval_seconds": 1800,
    "last_polled_at": "2026-06-23T20:33:07.123456+08:00",
    "last_error": "",
    "created_at": "2026-06-23T20:33:07.123456+08:00",
    "updated_at": "2026-06-23T20:33:07.123456+08:00",
    "groups": []
  }
]
```

### 更新上游

`PUT /api/upstreams/:id`

请求：

```json
{
  "name": "新的名称",
  "kind": "new_api",
  "url": "https://new-api.example.com",
  "balance_auth_type": "newapi_access_token",
  "balance_user_id": "1",
  "balance_access_token": "new-api-user-access-token",
  "call_url": "https://new-api.example.com",
  "call_key": "sk-your-call-key",
  "enabled": true,
  "poll_interval_seconds": 1800,
  "groups": [
    {
      "name": "default",
      "display_name": "默认分组",
      "test_model": "gpt-4o-mini",
      "manual_ratio": 0.25,
      "enabled": true
    }
  ]
}
```

说明：

- 传任意 `balance_*`、`auth_username/auth_password`、`call_url/call_key`、`api_key` 等凭证字段：重新加密保存，并更新 `auth_secret_masked`。
- 不传任何凭证字段：不修改已保存凭证。
- `groups` 不传：不修改分组。
- `groups` 传入数组：以后端收到的数组为准重建该上游所有分组。

响应 `data`：同“添加上游”。

### 设置分组倍率

`PUT /api/upstreams/:id/groups/:group_name/ratio`

请求：

```json
{
  "ratio": 0.25
}
```

说明：

- 用于前端手动设置某个上游分组的倍率。
- `group_name` 使用上游返回的分组名，例如 `default`、`Codex-Pro`。
- 如果分组不存在，后端会自动创建一个轻量分组配置。
- 手动倍率会保存到 `upstream_groups.manual_ratio`，刷新监控项时作为唯一倍率来源。
- 传 `{"ratio": null}` 可以清空手动倍率。

响应 `data`：

```json
{
  "id": 1,
  "upstream_id": 1,
  "name": "default",
  "display_name": "default",
  "manual_ratio": 0.25,
  "enabled": true,
  "created_at": "2026-06-23T20:33:07.123456+08:00",
  "updated_at": "2026-06-23T20:33:07.123456+08:00"
}
```

### 删除上游

`DELETE /api/upstreams/:id`

响应 `data`：

```json
{
  "deleted": true
}
```

说明：

- 会级联删除该上游的分组、监控项、检查日志和配置快照。

### 刷新单个上游

`POST /api/upstreams/:id/refresh`

行为：

- 后台触发该上游刷新。
- 接口立即返回。
- 同一个上游同一时间只允许一个刷新任务。

响应 `data`：

```json
{
  "running": true,
  "upstream_id": 1
}
```

### 测试上游

`POST /api/upstreams/:id/test`

行为：

- 同步执行一次该上游刷新。
- 上游请求失败时返回 `502`。

成功响应 `data`：

```json
{
  "ok": true
}
```

## 监控项接口

### 刷新单个监控项

`POST /api/items/:id/refresh`

当前实现：

- 根据该监控项找到所属上游。
- 后台触发整个上游刷新。

响应 `data`：

```json
{
  "running": true,
  "item_id": 1
}
```

## 前端建议

- token 存在内存或本地存储均可，收到 `401` 时跳回登录页。
- `POST /api/dashboard/refresh` 和 `POST /api/upstreams/:id/refresh` 返回后，可以轮询 `GET /api/dashboard`。
- 添加上游主表单建议展示：类型、后台 URL、余额认证方式、余额凭证、调用 URL、调用密钥、测试模型。
- 后台 URL 用于余额查询；调用 URL 和调用密钥用于真实流式请求可用性验证。
- new-api 推荐展示：用户 access token + 用户 ID。
- sub2api 推荐展示：refresh token，access token 作为可选缓存字段。
- 账号密码登录放在兼容/高级区域，提示可能受 Turnstile、Cloudflare 或验证码影响。
- 不要在界面保存或展示密钥明文；新增/更新后只使用后端返回的 `auth_secret_masked`。
- `ratio`、`balance` 等小数字段按字符串展示，需要计算时前端再转数字。

## 上游余额与可用性调用

### new-api

主流程保存：

- `url`
- `balance_auth_type = newapi_access_token`
- `balance_access_token`
- `balance_user_id`
- `call_url`
- `call_key`

new-api access token 获取方式：

```http
POST /api/user/token
```

在 new-api 后台生成用户 access token 后，前端把 token 填入 `balance_access_token`，把用户 ID 填入 `balance_user_id`。

余额查询：

```http
GET /api/user/self
Authorization: Bearer <balance_access_token>
New-Api-User: <balance_user_id>
```

解析：

- `quota / 500000` -> `remaining`
- `used_quota / 500000` -> `used`
- `(quota + used_quota) / 500000` -> `total`
- `planName` 或 `group` -> `group`
- `unit = USD`

兼容模式：

- `balance_auth_type = password` 时，后端会尝试 `POST /api/user/login`，再用临时 session 查询余额。
- 旧 `auth_type = new_api_token` 仍可兼容已有管理 token 配置。

错误状态：

- `balance credential invalid`：余额 access token、用户 ID 或会话失效。
- 余额失败不会影响 `call_url + call_key` 可用性检测。

### sub2api

主流程保存：

- `url`
- `balance_auth_type = sub2api_refresh_token`
- `balance_refresh_token`
- `balance_access_token`，可选缓存
- `balance_token_expires_at`，可选缓存
- `call_url`
- `call_key`

sub2api refresh token 获取方式：

- 前端可在用户登录 sub2api 后，从登录响应或浏览器本地保存的认证信息中取得 refresh token。
- 后端不依赖账号密码作为主流程，因为很多部署会启用 Turnstile、Cloudflare 或验证码。

刷新 access token：

```http
POST /api/v1/auth/refresh
Content-Type: application/json

{
  "refresh_token": "<balance_refresh_token>"
}
```

兼容响应：

```json
{ "access_token": "...", "refresh_token": "...", "expires_in": 86400 }
```

```json
{ "code": 0, "data": { "access_token": "...", "refresh_token": "...", "expires_in": 86400 } }
```

刷新成功后，后端会原子保存新的 `balance_access_token`、`balance_refresh_token` 和 `balance_token_expires_at`。

余额查询：

```http
GET /api/v1/auth/me
Authorization: Bearer <access_token>
```

如果 `/api/v1/auth/me` 没有额度字段，后端 fallback：

```http
GET /api/v1/user/profile
Authorization: Bearer <access_token>
```

额度字段兼容：

- 后端会识别 `balance`、`remaining_balance`、`remaining`、`quota`、`credit`、`credits`、`available`、`available_balance`、`left_quota`、`remain_quota` 等字段。
- 如果只有 `total/limit/quota_limit/total_quota` 和 `used/usage/current/total_used/used_quota`，后端会计算剩余额度。
- 如果仍解析不到，返回明确错误 `未找到额度字段`。

兼容模式：

- `balance_auth_type = password` 时，后端会尝试 `POST /api/v1/auth/login`。
- 旧 `auth_type = x_api_key` 仍可兼容已有管理接口配置。

错误状态：

- `balance credential invalid`：refresh token 失效或刷新失败。
- `未找到额度字段`：接口可访问，但响应里没有可识别余额字段。

### 可用性检测

所有上游类型都固定使用：

```http
POST {call_url}/v1/chat/completions
Authorization: Bearer <call_key>
Content-Type: application/json
Accept: text/event-stream
```

请求体：

```json
{
  "model": "<groups[].test_model>",
  "messages": [{ "role": "user", "content": "hi" }],
  "stream": true
}
```

说明：

- 前端只传 `groups[].test_model`。
- prompt 固定 `hi`。
- stream 固定 `true`。
- endpoint 固定 `chat_completions`。
- 后端识别 SSE `data:` chunk 和 `[DONE]`。
- 失败时 `last_message` 会记录 HTTP 状态和上游错误 `message`，但不会泄露 `call_key`。
- 倍率只使用前端设置的 `manual_ratio`；未设置时 `ratio` 留空。
