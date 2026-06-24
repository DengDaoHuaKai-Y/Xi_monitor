# 实施计划

## 阶段 1：最小后端

目标：Go + Gin + PostgreSQL 跑起来。

任务：

- 初始化 `backend`。
- 读取 `config.yaml`。
- 连接 PostgreSQL。
- 建表：`upstreams`、`upstream_groups`、`monitor_items`、`check_logs`、`upstream_config_snapshots`。
- 实现 AES-GCM 加密模块。
- 实现登录接口。
- 实现 `POST /api/upstreams`，用于添加上游账号/实例并加密保存凭证。
- 实现 `GET /api/upstreams`，只返回脱敏凭证。
- 保存上游分组配置：一个上游账号对应多个分组，每个分组有自己的模型、端点类型、提示词、模式、是否流式。

验收：

- 可以登录。
- 可以写入加密后的上游凭证。
- 数据库里看不到明文 token。
- 可以添加一个 new-api 或 sub2api 上游，并配置多个分组。

## 阶段 2：对接 new-api

目标：能把 new-api 渠道展示到仪表盘。

任务：

- 调用 `GET /api/channel/` 获取渠道。
- 调用 `GET /api/channel/test/:id?model=...&endpoint_type=...&stream=...` 获取可用性。
- 调用 `GET /api/channel/update_balance/:id` 获取余额。
- 调用 `GET /api/ratio_config` 获取倍率，接口未开启时只记录提示，不阻塞仪表盘。
- 写入 `monitor_items` 和 `check_logs`。

验收：

- 仪表盘能看到 new-api 渠道。
- 能看到状态、延迟、倍率、余额、最近检测时间。

## 阶段 3：对接 sub2api

目标：能把 sub2api 账号和监控展示到仪表盘。

任务：

- 调用 `GET /api/v1/admin/accounts` 获取账号。
- 调用 `POST /api/v1/admin/accounts/:id/test` 测试账号，请求体带 `model_id`、`prompt`、`mode`。
- 调用 `GET /api/v1/admin/accounts/:id/usage` 获取余额/额度。
- 调用 `GET /api/v1/admin/channels` 和 `GET /api/v1/admin/groups` 获取倍率/定价摘要。
- 可选调用 `GET /api/v1/admin/channel-monitors` 获取监控项。
- 写入 `monitor_items` 和 `check_logs`。

验收：

- 仪表盘能看到 sub2api 账号或监控项。
- 能看到状态、延迟、倍率、余额、最近检测时间。

## 阶段 4：轮询和手动刷新

目标：30 分钟自动刷新，按钮可手动刷新。

任务：

- 实现后台 poller。
- 默认每 `1800` 秒刷新一次。
- 实现 `POST /api/dashboard/refresh`。
- 手动刷新只触发检测，使用已保存的分组测试参数，不临时覆盖。
- 防止同一上游并发刷新。
- 记录刷新错误，但不清空上一轮有效数据。

验收：

- 后台能自动刷新。
- 点击刷新按钮能立即刷新。
- 刷新失败时仪表盘仍显示上一轮数据和错误提示。

## 阶段 5：Vue 仪表盘

目标：做出类似截图的单页面板。

页面：

- 登录页。
- 仪表盘页。

仪表盘元素：

- 顶部标题。
- 时间维度按钮：`6h`、`24h`、`7d`、`30d`。
- 监控中状态。
- 水印开关可以先不做。
- 刷新按钮。
- 添加上游弹窗。
- 表格。
- 简单曲线。
- 剩余余额列。

验收：

- 页面风格接近截图。
- 表格在桌面端清晰。
- 登录后进入仪表盘。
- 未登录访问仪表盘会跳回登录页。

## 最终 MVP

完成后项目只包含：

- 一个登录页。
- 一个仪表盘页。
- 一个后台 poller。
- PostgreSQL 加密保存上游凭证。
- 对接 `new-api` 和 `sub2api` 现有接口。
