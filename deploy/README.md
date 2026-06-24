# Production Deploy

服务器只需要这个目录里的生产部署文件，不需要源码、Node、Go 或 Docker build 环境。

默认配置使用预构建镜像：

```bash
ghcr.io/dengdaohuakai-y/xi_monitor:latest
```


## 文件

- `docker-compose.prod.yml`：生产 compose 模板，服务器使用时建议改名为 `docker-compose.yml`
- `.env.example`：生产环境变量模板，服务器使用时复制为 `.env`
- `deploy.sh`：更新脚本，执行 pull、up 和旧镜像清理

## 首次部署

```bash
mkdir -p /opt/xi_monitor
cd /opt/xi_monitor
```

把 `docker-compose.prod.yml` 放到服务器并命名为 `docker-compose.yml`，把 `.env.example` 放到服务器并命名为 `.env`。

修改 `.env` 里的参数：

- `XI_MONITOR_IMAGE`：镜像地址。使用默认镜像时不用改；使用 fork 仓库构建的镜像时改成 `ghcr.io/<github-owner>/xi_monitor:latest`
- `XI_MONITOR_HTTP_PORT`：宿主机访问端口，默认 `80`
- `POSTGRES_PASSWORD`：数据库密码，必须改成强密码
- `MID_DATABASE_DSN`：把里面的数据库密码同步改成同一个 `POSTGRES_PASSWORD`
- `MID_SESSION_SECRET`：会话密钥，必须改成随机长字符串
- `MID_ENCRYPTION_KEY`：数据加密 key，必须改成稳定的 32 字节字符串或 base64 编码的 32 字节 key
- `MID_ADMIN_USERNAME`：管理员用户名，默认 `admin`
- `MID_ADMIN_PASSWORD_HASH`：管理员密码 hash，必须改
- `MID_POLLER_INTERVAL_SECONDS`：轮询间隔秒数，默认 `1800`

生成管理员密码 hash：

```bash
docker run --rm ghcr.io/dengdaohuakai-y/xi_monitor:latest -hash-password "管理员密码"
```

```bash
docker compose pull
docker compose up -d
```

## 私有镜像登录

如果 GHCR 镜像是私有的，服务器需要先登录：

```bash
docker login ghcr.io
```

用户名填 GitHub 用户名，密码填有 `read:packages` 权限的 Personal Access Token。

## 更新

```bash
cd /opt/xi_monitor
docker compose pull
docker compose up -d
docker image prune -f
```
