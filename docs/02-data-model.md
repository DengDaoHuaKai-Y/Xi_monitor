# 最小数据表设计

## 设计原则

- 只保存面板需要的数据。
- 上游敏感凭证加密保存。
- 不做复杂用户体系。
- 数据库作为缓存和历史记录，不替代上游系统。

## upstreams

保存被监控的 `new-api` / `sub2api` 实例。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGSERIAL | 主键 |
| name | VARCHAR(100) | 显示名称 |
| kind | VARCHAR(32) | `new_api` / `sub2api` |
| base_url | TEXT | 上游地址 |
| auth_type | VARCHAR(32) | `bearer` / `cookie` / `admin_api_key` |
| auth_secret_ciphertext | TEXT | 加密后的凭证 |
| auth_secret_nonce | TEXT | AES-GCM nonce |
| auth_secret_masked | VARCHAR(120) | 脱敏展示 |
| enabled | BOOLEAN | 是否启用 |
| poll_interval_seconds | INT | 默认 1800 |
| last_polled_at | TIMESTAMPTZ | 最近轮询 |
| last_error | TEXT | 最近错误 |
| created_at | TIMESTAMPTZ | 创建时间 |
| updated_at | TIMESTAMPTZ | 更新时间 |

## upstream_groups

一个上游账号可以配置多个分组。分组里的测试参数在添加或编辑时手动设置，保存后轮询和刷新都按这个固定配置执行，不自动从上游改写。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGSERIAL | 主键 |
| upstream_id | BIGINT | 关联 upstreams |
| name | VARCHAR(100) | 分组名称，例如 `default`、`GPT Pro` |
| display_name | VARCHAR(100) | 展示名称，可空 |
| test_model | VARCHAR(200) | 测试模型 |
| test_endpoint_type | VARCHAR(64) | 测试端点类型 |
| test_prompt | TEXT | 测试提示词 |
| test_mode | VARCHAR(64) | sub2api 测试模式 |
| test_stream | BOOLEAN | 是否流式测试 |
| enabled | BOOLEAN | 是否启用该分组 |
| created_at | TIMESTAMPTZ | 创建时间 |
| updated_at | TIMESTAMPTZ | 更新时间 |

唯一索引：

- `(upstream_id, name)`

## monitor_items

仪表盘表格行。来自 new-api channel、sub2api account、sub2api monitor。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGSERIAL | 主键 |
| upstream_id | BIGINT | 关联 upstreams |
| upstream_group_id | BIGINT | 关联 upstream_groups，可空 |
| external_id | VARCHAR(128) | 上游资源 ID |
| item_type | VARCHAR(32) | `channel` / `account` / `monitor` |
| name | VARCHAR(200) | API/账号/渠道名称 |
| endpoint | TEXT | 上游地址或资源地址 |
| source | VARCHAR(32) | `new-api` / `sub2api` |
| group_name | VARCHAR(100) | 分组 |
| ratio | NUMERIC(12,6) | 倍率 |
| status | VARCHAR(32) | `unknown` / `available` / `unavailable` / `error` |
| latency_ms | INT | 最新延迟 |
| availability_percent | NUMERIC(6,2) | 可用率 |
| balance | NUMERIC(20,8) | 剩余余额 |
| balance_unit | VARCHAR(32) | `USD` / `CNY` / `points` / 空 |
| last_checked_at | TIMESTAMPTZ | 最近检测 |
| last_message | TEXT | 最近信息 |
| trend | JSONB | 简单曲线数据 |
| raw_summary | JSONB | 脱敏后的摘要 |
| created_at | TIMESTAMPTZ | 创建时间 |
| updated_at | TIMESTAMPTZ | 更新时间 |

唯一索引：

- `(upstream_id, upstream_group_id, item_type, external_id)`

## check_logs

保存简短历史，用于曲线和排查。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGSERIAL | 主键 |
| monitor_item_id | BIGINT | 关联 monitor_items |
| check_type | VARCHAR(32) | `sync` / `availability` / `balance` / `ratio` |
| status | VARCHAR(32) | 结果状态 |
| test_params | JSONB | 本次测试使用的分组、模型、端点等参数 |
| latency_ms | INT | 耗时 |
| balance | NUMERIC(20,8) | 本次余额 |
| ratio | NUMERIC(12,6) | 本次倍率 |
| message | TEXT | 摘要 |
| checked_at | TIMESTAMPTZ | 检测时间 |

索引：

- `(monitor_item_id, checked_at DESC)`
- `(checked_at DESC)`

## upstream_config_snapshots

保存倍率/价格配置 hash，用来检测上游倍率是否变化。只保留轻量设计。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | BIGSERIAL | 主键 |
| upstream_id | BIGINT | 上游 |
| config_hash | CHAR(64) | 规范化 JSON sha256 |
| config_summary | JSONB | 脱敏摘要 |
| diff_summary | JSONB | 与上次相比的变化摘要 |
| changed | BOOLEAN | 是否变化 |
| checked_at | TIMESTAMPTZ | 检查时间 |

## 加密说明

上游凭证写入流程：

1. 从表单或初始化脚本接收明文。
2. 生成随机 12 字节 nonce。
3. 使用 AES-256-GCM 加密。
4. 存储 `ciphertext` 和 `nonce`。
5. 存储脱敏值用于页面展示。
6. 明文只存在内存中，使用后丢弃。

不保存：

- 明文 token
- 明文 API key
- 明文 refresh token
- 完整 Authorization Header

## 可用性测试参数

测试可用性时必须带上可控参数，否则测试可能使用错模型或错分组。

参数优先级：

1. `upstream_groups` 上保存的分组级参数。
2. 上游系统自己的默认值。

手动刷新只触发检测，不临时覆盖测试参数。需要变更模型、分组、端点类型等参数时，应编辑上游分组配置。

new-api 参数映射：

| Mid Station 参数 | new-api 参数 |
| --- | --- |
| `upstream_groups.test_model` | query `model` |
| `upstream_groups.test_endpoint_type` | query `endpoint_type` |
| `upstream_groups.test_stream` | query `stream` |
| `upstream_groups.name` | 本地分组归类；new-api 当前测试接口内部使用系统用户组 |

sub2api 参数映射：

| Mid Station 参数 | sub2api 参数 |
| --- | --- |
| `upstream_groups.test_model` | body `model_id` |
| `upstream_groups.test_prompt` | body `prompt` |
| `upstream_groups.test_mode` | body `mode` |
| `upstream_groups.name` | 用于筛选账号列表或本地展示；单账号测试接口按 account id 执行 |
