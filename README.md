# Xi Monitor

Xi Monitor 是一个用于监控上游 API 渠道可用性、延迟、倍率和余额的轻量面板。后端使用 Go + Gin，前端使用 Vue 3 + Vite，生产环境通过 Docker Compose 运行预构建镜像。

## 功能

- 监控 `new-api`、`sub2api` 等上游实例
- 展示渠道状态、延迟、首 Token 耗时、倍率、余额和最近检测时间
- 支持手动刷新单个上游或全部上游
- 支持为不同分组配置测试模型、端点类型、提示词和轮询参数
- 上游 token、key、密码等敏感信息加密保存到 PostgreSQL
- 提供 GitHub Actions 构建的 GHCR 镜像，服务器无需安装 Node、Go 或执行 Docker build

## 快速部署

推荐在服务器上创建独立目录：

```bash
mkdir -p /opt/xi_monitor
cd /opt/xi_monitor
```

从仓库复制生产部署文件：

- `deploy/docker-compose.prod.yml` 复制为 `/opt/xi_monitor/docker-compose.yml`
- `deploy/.env.example` 复制为 `/opt/xi_monitor/.env`

默认镜像：

```bash
ghcr.io/dengdaohuakai-y/xi_monitor:latest
```

修改 `.env` 后启动：

```bash
docker compose pull
docker compose up -d
```

访问地址：

```text
http://服务器IP
```

如果修改了 `XI_MONITOR_HTTP_PORT`，访问地址改为对应端口，例如 `http://服务器IP:8080`。

## 环境变量

部署前至少需要修改以下参数：

| 变量 | 说明 |
| --- | --- |
| `XI_MONITOR_IMAGE` | 应用镜像地址，默认 `ghcr.io/dengdaohuakai-y/xi_monitor:latest` |
| `XI_MONITOR_HTTP_PORT` | 对外暴露的 HTTP 端口，默认 `80` |
| `POSTGRES_PASSWORD` | PostgreSQL 数据库密码，必须改为强密码 |
| `MID_DATABASE_DSN` | 数据库连接串，其中的密码需要与 `POSTGRES_PASSWORD` 保持一致 |
| `MID_SESSION_SECRET` | 登录会话密钥，建议使用随机长字符串 |
| `MID_ENCRYPTION_KEY` | 敏感信息加密 key，使用稳定的 32 字节字符串或 base64 编码的 32 字节 key |
| `MID_ADMIN_USERNAME` | 管理员用户名，默认 `admin` |
| `MID_ADMIN_PASSWORD_HASH` | 管理员密码 hash，必须替换 |
| `MID_POLLER_INTERVAL_SECONDS` | 后台轮询间隔，单位秒，默认 `1800` |

生成管理员密码 hash：

```bash
docker run --rm ghcr.io/dengdaohuakai-y/xi_monitor:latest -hash-password "管理员密码"
```

将输出结果写入 `.env` 的 `MID_ADMIN_PASSWORD_HASH`。

## 更新

```bash
cd /opt/xi_monitor
docker compose pull
docker compose up -d
docker image prune -f
```

也可以使用 `deploy/deploy.sh` 执行同样的更新流程。

## 目录结构

- `backend/`：Go + Gin 后端
- `frontend/`：Vue 3 + Vite 前端
- `deploy/`：生产部署文件
- `docs/`：架构、接口和数据模型文档
- `.github/workflows/docker.yml`：GitHub Actions 镜像构建流程
- `Dockerfile`：生产镜像构建文件
- `docker-compose.yml`：本地开发和本地验证使用

## 生产部署原则

生产服务器只运行已经构建好的镜像，不在服务器上编译前端、编译后端或构建镜像。

服务器只需要执行：

```bash
docker compose pull
docker compose up -d
docker image prune -f
```

不需要执行：

```bash
npm install
npm run build
go build
docker build
```
