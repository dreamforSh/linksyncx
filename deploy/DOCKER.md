# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  --restart unless-stopped \
  --stop-timeout 90 \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  ghcr.io/dreamforsh/linksyncx:latest
```

`--stop-timeout 90` gives in-flight requests and the usage/billing flush time to
finish when the container stops, and `--restart unless-stopped` brings it back
after an in-app restart. See Graceful Shutdown below.

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: ghcr.io/dreamforsh/linksyncx:latest
    restart: unless-stopped
    stop_grace_period: 90s
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/sub2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    restart: unless-stopped
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Startup and Database Recovery

Sub2API runs database migrations while starting. PostgreSQL may still be
recovering briefly after a host or Docker daemon restart. The application
retries transient PostgreSQL startup and connection errors with bounded
exponential backoff, then continues startup when the database is ready.
Permanent errors such as invalid credentials, migration checksum mismatches,
SQL errors, and incompatible data fail immediately.

The Compose deployment also checks PostgreSQL readiness with both `pg_isready`
and a simple SQL query. `depends_on: condition: service_healthy` helps order a
fresh Compose start, but application-level retries are still required when
Docker restores existing containers after a host restart.

## Graceful Shutdown / 优雅关停

`docker stop` 发出 SIGTERM 后，Sub2API 立即关闭监听并断开 WebSocket，进行中的
HTTP/SSE 请求最多再等 `SERVER_SHUTDOWN_TIMEOUT_SECONDS`（默认 30 秒）；之后强制取消约
5 秒、执行依赖清理（usage/计费落盘，最多 30 秒）再退出。Docker 默认只等 10 秒就
SIGKILL，所以容器的停止超时必须不小于“宽限 + 35 秒”：

```bash
docker run -d --restart unless-stopped --stop-timeout 90 -e SERVER_SHUTDOWN_TIMEOUT_SECONDS=30 ... ghcr.io/dreamforsh/linksyncx:latest
```

Compose 中对应 `stop_grace_period: 90s`（仓库自带的 compose 文件读取 `.env` 中的
`SUB2API_STOP_GRACE_PERIOD`）。自带的 compose 文件把 `SERVER_SHUTDOWN_TIMEOUT_SECONDS`
默认留空透传：留空时生效的是挂载的 `config.yaml` 中的 `server.shutdown_timeout_seconds`
（未配置则为 30），在 `.env` 中填写后覆盖 `config.yaml`。管理后台的“重启”按钮走同一流程，
并以退出码 0 退出，依赖 `restart: unless-stopped` 重新拉起容器。

升级已有部署时，停止超时不会跟着镜像一起更新：`docker compose pull && docker compose up -d`
不会替换 compose 文件，早先的 compose 文件里 `sub2api` 服务没有 `stop_grace_period`，
`.env` 中的 `SUB2API_STOP_GRACE_PERIOD` 也就不起作用，需要按上方示例手动补上；
`docker run` 部署在重新创建容器时带上 `--stop-timeout 90`。

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |
| `SERVER_SHUTDOWN_TIMEOUT_SECONDS` | Grace for in-flight HTTP/SSE requests on stop/restart (1-3600) | No | `30` |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## Links

- [GitHub Repository](https://github.com/LinkSyncX/linksyncx)
- [Documentation](https://github.com/LinkSyncX/linksyncx#readme)
