# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  ghcr.io/dreamforsh/linksyncx:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: ghcr.io/dreamforsh/linksyncx:latest
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
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
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
docker run -d --stop-timeout 90 -e SERVER_SHUTDOWN_TIMEOUT_SECONDS=30 ... ghcr.io/dreamforsh/linksyncx:latest
```

Compose 中对应 `stop_grace_period: 90s`（仓库自带的 compose 文件读取 `.env` 中的
`SUB2API_STOP_GRACE_PERIOD`）。管理后台的“重启”按钮走同一流程，并以退出码 0 退出，
依赖 `restart: unless-stopped` 重新拉起容器。

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
