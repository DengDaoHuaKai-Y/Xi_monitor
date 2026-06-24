# 面板自身 API

这些接口只给 Vue 面板调用，不对外公开。

## 认证

### 登录

`POST /api/auth/login`

请求：

```json
{
  "username": "admin",
  "password": "your-password"
}
```

响应：

```json
{
  "token": "jwt-token"
}
```

说明：

- 用户名和密码 hash 来自配置文件。
- 不提供注册接口。

### 当前用户

`GET /api/auth/me`

### 退出

`POST /api/auth/logout`

## 仪表盘

### 获取仪表盘数据

`GET /api/dashboard`

响应：

```json
{
  "summary": {
    "total": 10,
    "available": 8,
    "unavailable": 2,
    "avg_latency_ms": 820,
    "last_refreshed_at": "2026-06-23T18:30:00+08:00"
  },
  "items": [
    {
      "id": 1,
      "name": "5YuanToken",
      "endpoint": "https://5yuantoken.org",
      "source": "new-api",
      "group_name": "GPT Pro",
      "ratio": "0.120000",
      "status": "available",
      "latency_ms": 535,
      "availability_percent": "99.00",
      "balance": "12.50000000",
      "balance_unit": "USD",
      "last_checked_at": "2026-06-23T18:39:13+08:00",
      "trend": [12, 15, 8, 20]
    }
  ]
}
```

## 手动刷新

### 刷新全部

`POST /api/dashboard/refresh`

行为：

- 立即执行一次完整刷新。
- 包含资源同步、可用性测试、余额刷新、倍率检查。
- 如果当前已有刷新任务在跑，直接返回当前任务状态。

刷新使用数据库中已保存的上游分组测试参数，不支持临时覆盖。需要变更模型、分组或端点时，先编辑上游分组配置。

### 刷新单个上游

`POST /api/upstreams/:id/refresh`

### 刷新单个项目

`POST /api/items/:id/refresh`

刷新使用该项目关联的 `upstream_groups` 配置。

## 上游账号配置

因为页面只有登录和仪表盘，第一版不做单独配置页面。仪表盘内放一个“添加上游”弹窗，调用下面这些接口。

### 添加上游账号/实例

`POST /api/upstreams`

请求：

```json
{
  "name": "我的 new-api",
  "kind": "new_api",
  "base_url": "https://new-api.example.com",
  "auth_type": "bearer",
  "auth_secret": "admin-token",
  "groups": [
    {
      "name": "GPT Pro",
      "display_name": "GPT Pro",
      "test_model": "gpt-4o-mini",
      "test_endpoint_type": "chat_completions",
      "test_prompt": "ping",
      "test_mode": "chat",
      "test_stream": false,
      "enabled": true
    },
    {
      "name": "GPT 混合",
      "test_model": "gpt-4o",
      "test_endpoint_type": "responses",
      "test_prompt": "ping",
      "test_mode": "chat",
      "test_stream": false,
      "enabled": true
    }
  ],
  "enabled": true
}
```

后端必须加密 `auth_secret` 后入库。

字段说明：

- `name`：面板显示名称。
- `kind`：`new_api` 或 `sub2api`。
- `base_url`：上游后台地址。
- `auth_type`：认证方式，第一版支持 `bearer`、`cookie`、`admin_api_key`。
- `auth_secret`：上游管理凭证，写入数据库前必须加密。
- `groups`：该上游账号下的一组分组配置，一个上游可以有多个分组。
- `groups[].name`：分组名称，用于归类和筛选。
- `groups[].test_model`：该分组测试模型。new-api 会作为 query `model`，sub2api 会作为 body `model_id`。
- `groups[].test_endpoint_type`：该分组端点类型。new-api 会作为 query `endpoint_type`。
- `groups[].test_prompt`：该分组测试提示词。sub2api 会作为 body `prompt`。
- `groups[].test_mode`：该分组测试模式。sub2api 会作为 body `mode`。
- `groups[].test_stream`：该分组是否使用流式测试。new-api 会作为 query `stream`。
- `enabled`：是否启用轮询。

保存成功后建议立即触发一次刷新，把上游里的数据按分组拉到仪表盘。保存后的分组测试参数不会被轮询自动变更。

### 上游列表

`GET /api/upstreams`

响应不返回明文，只返回 `auth_secret_masked`。

### 更新上游

`PUT /api/upstreams/:id`

说明：

- `auth_secret` 不传或为空表示不修改凭证。
- 如果传了新的 `auth_secret`，后端重新加密保存。
- `groups` 不传表示不修改分组配置。
- 如果传入 `groups`，以后端收到的数组为准更新分组配置。

### 删除上游

`DELETE /api/upstreams/:id`

### 测试上游连接

`POST /api/upstreams/:id/test`

用于保存前或保存后测试 token/cookie 是否可用。

## 上游接口调用原则

### new-api

优先调用：

- `GET /api/channel/`
- `GET /api/channel/test/:id?model={model}&endpoint_type={endpoint_type}&stream={stream}`
- `GET /api/channel/update_balance/:id`
- `GET /api/ratio_config`

new-api 测试参数：

| 参数 | 来源 | 说明 |
| --- | --- | --- |
| `model` | `upstream_groups.test_model` | 测试模型 |
| `endpoint_type` | `upstream_groups.test_endpoint_type` | 端点类型 |
| `stream` | `upstream_groups.test_stream` | 是否流式 |

### sub2api

优先调用：

- `GET /api/v1/admin/accounts`
- `POST /api/v1/admin/accounts/:id/test`
- `GET /api/v1/admin/accounts/:id/usage`
- `GET /api/v1/admin/channels`
- `GET /api/v1/admin/groups`
- `GET /api/v1/admin/channel-monitors`
- `POST /api/v1/admin/channel-monitors/:id/run`

sub2api 账号测试请求体：

```json
{
  "model_id": "claude-sonnet-4",
  "prompt": "ping",
  "mode": "chat"
}
```

参数来源：

| 参数 | 来源 | 说明 |
| --- | --- | --- |
| `model_id` | `upstream_groups.test_model` | 测试模型 |
| `prompt` | `upstream_groups.test_prompt` | 测试提示词 |
| `mode` | `upstream_groups.test_mode` | 测试模式 |

## 响应规范

简单即可：

```json
{
  "success": true,
  "message": "",
  "data": {}
}
```

错误：

```json
{
  "success": false,
  "message": "upstream request failed"
}
```
