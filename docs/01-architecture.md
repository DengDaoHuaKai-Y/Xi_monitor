# 轻量架构与范围

## 核心定位

Mid Station 只是你的个人监控面板。它不承担用户系统、不做公开服务、不替代 `new-api` 或 `sub2api` 后台。

它做四件事：

1. 登录鉴权。
2. 加密保存上游连接凭证。
3. 定时调用 `new-api` / `sub2api` 现有接口。
4. 在仪表盘展示状态、延迟、倍率、余额。

## 页面

```text
/login       登录页
/dashboard   仪表盘，包含添加上游弹窗
```

不做：

- 注册页
- 用户管理页
- 多角色权限
- 公开状态页
- 复杂告警中心
- 多租户

## 添加上游账号

虽然页面只有登录和仪表盘，但仪表盘需要有一个“添加上游”弹窗。它用于添加要监控的 `new-api` / `sub2api` 管理后台连接信息。

流程：

1. 你在仪表盘点击“添加上游”。
2. 输入上游名称、类型、地址、认证方式、管理凭证。
3. 添加一个或多个分组配置，每个分组配置自己的模型、端点类型、提示词、模式、是否流式。
4. 前端调用 `POST /api/upstreams`。
5. 后端用 AES-256-GCM 加密管理凭证后写入 PostgreSQL。
6. 后端保存 `upstream_groups`，形成一个上游账号对多个分组的一对多关系。
7. 后端立即刷新该上游，把现有 channel/account/monitor 按已保存分组配置拉取成仪表盘数据。

注意：

- 这个“添加上游”是 Mid Station 自己的功能。
- `new-api` / `sub2api` 的参考接口只用于采集数据，不负责把上游添加到 Mid Station。
- API 永远不返回明文凭证，只返回脱敏值。
- 分组测试参数以数据库保存值为准；轮询和手动刷新都不自动修改这些参数。

## 架构

```mermaid
flowchart LR
    Browser["Vue Dashboard"] --> Backend["Go Gin API"]
    Backend --> DB["PostgreSQL"]
    Backend --> Scheduler["30 min Poller"]
    Scheduler --> NewAPI["new-api existing APIs"]
    Scheduler --> Sub2API["sub2api existing APIs"]
```

## 后端职责

推荐目录：

```text
backend/
  cmd/server/main.go
  internal/config/
  internal/http/
  internal/auth/
  internal/crypto/
  internal/store/
  internal/upstream/
    newapi/
    sub2api/
  internal/poller/
```

职责划分：

- `config`：读取配置文件和环境变量。
- `auth`：唯一管理员登录、JWT 或 session。
- `crypto`：加密/解密上游凭证。
- `store`：PostgreSQL 读写。
- `upstream/newapi`：调用 new-api 现有接口。
- `upstream/sub2api`：调用 sub2api 现有接口。
- `poller`：每 30 分钟刷新一次，也支持手动触发。

## 配置文件

示例：

```yaml
server:
  addr: ":8080"

security:
  session_secret: "change-me-session-secret"
  encryption_key: "32-bytes-base64-or-env-ref"

admin:
  username: "admin"
  password_hash: "$2a$12$..."

database:
  dsn: "postgres://mid_station:password@localhost:5432/mid_station?sslmode=disable"

poller:
  interval_seconds: 1800
```

说明：

- 管理员账号从配置文件初始化。
- 不提供注册。
- `encryption_key` 必须稳定保存，丢失后数据库里的上游凭证无法解密。

## 上游凭证安全

敏感信息包括：

- new-api 管理 token / cookie
- sub2api 管理 token / cookie
- 上游账号密钥
- access token / refresh token

保存要求：

- 入库前使用 AES-256-GCM 加密。
- 数据库只保存密文、nonce、脱敏显示值。
- API 永远不返回明文。
- 日志过滤 `Authorization`、`token`、`api_key`、`access_token`、`refresh_token`。

## 仪表盘数据

仪表盘展示缓存结果，不让前端直接访问 `new-api` 或 `sub2api`。

后台刷新流程：

1. 解密上游管理凭证。
2. 调用现有上游接口。
3. 归一化为面板行数据。
4. 更新 PostgreSQL 缓存。
5. 前端读取 `/api/dashboard`。

## 轮询与手动刷新

- 自动轮询：默认 30 分钟。
- 手动刷新：点击仪表盘刷新按钮立即触发。
- 手动刷新和自动轮询使用同一套逻辑。
- 同一时间同一个上游只允许一个刷新任务，避免重复消耗 token。
