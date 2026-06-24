# Mid Station 自用上游监控面板

这是一个单人自用的 Web 面板，不做公开站点、不做注册、不做多用户权限系统。目标很简单：

- 一个登录页。
- 一个仪表盘页。
- 对接现有 `new-api` 和 `sub2api` 管理接口。
- 展示上游 API/账号/渠道的可用状态、延迟、倍率、剩余余额、最近检测时间。
- 后台默认每 30 分钟轮询一次。
- 页面提供手动刷新按钮。
- 上游账号、密码、管理 token、密钥等敏感信息必须加密保存到 PostgreSQL。

## 文档

- [01-architecture.md](./01-architecture.md)：轻量架构与页面范围
- [02-data-model.md](./02-data-model.md)：最小数据表设计
- [03-backend-api.md](./03-backend-api.md)：面板自身 API
- [04-implementation-plan.md](./04-implementation-plan.md)：分步实施计划

## 技术栈

- 后端：Go + Gin
- 数据库：PostgreSQL
- 前端：Vue 3 + TypeScript + Vite
- 认证：配置文件初始化唯一管理员账号
- 加密：AES-256-GCM，加密 key 从配置或环境变量读取

## 页面范围

### 登录页

- 用户名和密码。
- 用户只有你自己。
- 不提供注册入口。
- 初始账号写在配置文件里，例如 `config.yaml`。

### 仪表盘

参考你给的 Kan LLM 风格：表格为主，顶部有时间维度、监控状态、刷新按钮。

仪表盘内需要提供一个轻量的“添加上游”弹窗，不单独做配置页面。你可以在这里添加 `new-api` 或 `sub2api` 的后台地址和登录账号密码。凭证提交到后端后必须立即加密保存到 PostgreSQL，前端和接口都不再返回明文。

添加上游时需要一起配置这个上游下要监控的分组。一个上游账号可以对应多个分组，每个分组保存自己的测试模型、端点类型、提示词、测试模式等参数。保存后这些参数默认不自动变更，后续轮询和手动刷新都使用数据库里保存的配置。

建议列：

- API 名称
- 上游地址
- 来源：`new-api` / `sub2api`
- 分组
- 倍率
- 最新状态
- 延迟
- 可用率
- 剩余余额
- 最近检测时间
- 简单曲线

## 轮询策略

- 所有自动轮询默认 30 分钟一次。
- 可用性测试可能消耗 token，也按 30 分钟。
- 余额、倍率、资源同步也统一 30 分钟，避免复杂调度。
- 仪表盘提供手动刷新按钮，可以立即刷新。

## 对接原则

- `new-api` 和 `sub2api` 已有接口就直接调用，不重复实现它们的业务接口。
- Mid Station 只做“采集、缓存、展示、加密保存凭证”。
- 不直接读取两个上游项目的数据库。
- 不保存明文上游账号信息。

## 添加上游账号

这里的“上游账号”指你要监控的 `new-api` 或 `sub2api` 管理后台连接信息，不是普通用户注册。

添加方式：

- 仪表盘点击“添加上游”按钮。
- 填写名称、类型、后台地址、登录账号和登录密码。
- 调用 Mid Station 自己的 `POST /api/upstreams`。
- 后端加密保存凭证。
- 保存后立即执行一次手动刷新，把上游里的 channel/account/monitor 拉进仪表盘。

需要填写的字段：

- 名称：例如 `我的 new-api`
- 类型：`new_api` / `sub2api`
- 上游地址：例如 `https://new-api.example.com`
- 登录账号：上游后台账号，例如 `admin`
- 登录密码：上游后台密码
- 分组配置：一个或多个分组，例如 `default`、`GPT Pro`
- 每个分组的测试模型：例如 `gpt-4o-mini`、`claude-sonnet-4`
- 每个分组的端点类型：例如 `chat_completions`、`responses`、`embeddings`
- 每个分组的测试提示词和测试模式
- 每个分组是否流式测试，默认 `false`

后端保存后只展示脱敏值，例如 `admin`。

## Mid Station 自己的接口

- `POST /api/upstreams`：添加上游账号/实例，并加密保存凭证
- `GET /api/upstreams`：查看已添加上游，只返回脱敏凭证
- `PUT /api/upstreams/:id`：更新上游配置，默认不自动改已保存的分组测试参数
- `DELETE /api/upstreams/:id`：删除上游
- `POST /api/upstreams/:id/refresh`：手动刷新某个上游
- `POST /api/dashboard/refresh`：手动刷新全部上游

## 调用上游的参考接口

### new-api

- `GET /api/channel/`：渠道列表
- `GET /api/channel/test/:id?model=xxx&endpoint_type=xxx&stream=false`：测试单个渠道
- `GET /api/channel/update_balance/:id`：更新单个渠道余额
- `GET /api/ratio_config`：倍率配置，需上游开启暴露

### sub2api

- `GET /api/v1/admin/accounts`：账号列表
- `POST /api/v1/admin/accounts/:id/test`：测试账号，请求体支持 `model_id`、`prompt`、`mode`
- `GET /api/v1/admin/accounts/:id/usage`：账号用量/额度
- `GET /api/v1/admin/channels`：渠道和模型定价
- `GET /api/v1/admin/groups`：分组倍率
- `GET /api/v1/admin/channel-monitors`：监控列表
- `POST /api/v1/admin/channel-monitors/:id/run`：手动运行监控
