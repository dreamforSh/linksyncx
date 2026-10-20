#!/bin/sh
set -eu

# 优雅关停依赖部署侧的停止超时：Sub2API 收到 SIGTERM 后先等进行中的请求
# SERVER_SHUTDOWN_TIMEOUT_SECONDS 秒，再强制取消约 5 秒、执行依赖清理（最多 30 秒）。
# 任何部署方式的停止超时小于 宽限 + 35 秒，都会在 usage/计费落盘前被强杀。

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'shutdown deployment test failed: %s\n' "$1" >&2
  exit 1
}

# 工作区可能以 CRLF 检出（Windows），比较前统一去掉 CR。
normalized() {
  tr -d '\r' <"$1"
}

forced_and_cleanup_seconds=35
default_grace=30

# 宽限变量留空透传：非空的环境变量会覆盖挂载的 config.yaml，留空时 viper 回落到
# config.yaml 或默认的 30 秒。
grace_line="SERVER_SHUTDOWN_TIMEOUT_SECONDS="
[ "$(normalized deploy/.env.example | grep -Fxc "$grace_line" || true)" -eq 1 ] || \
  fail "deploy/.env.example must leave $grace_line empty exactly once so config.yaml keeps working"
[ "$(normalized deploy/.env.example | grep -c '^SERVER_SHUTDOWN_TIMEOUT_SECONDS=' || true)" -eq 1 ] || \
  fail "deploy/.env.example must define SERVER_SHUTDOWN_TIMEOUT_SECONDS exactly once"
stop_grace=$(normalized deploy/.env.example | sed -n 's/^SUB2API_STOP_GRACE_PERIOD=\([0-9][0-9]*\)s$/\1/p')
[ -n "$stop_grace" ] || fail "deploy/.env.example must set SUB2API_STOP_GRACE_PERIOD in seconds"
[ "$stop_grace" -ge $((default_grace + forced_and_cleanup_seconds)) ] || \
  fail "SUB2API_STOP_GRACE_PERIOD=${stop_grace}s is shorter than the default shutdown budget"

for compose_file in \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml \
  deploy/docker-compose.dev.yml
do
  counts=$(
    normalized "$compose_file" | awk -v stop="$stop_grace" '
      $0 == "  sub2api:" {
        in_application = 1
        next
      }
      in_application && $0 ~ /^  [A-Za-z0-9_-]+:$/ {
        in_application = 0
      }
      in_application && $0 == "    stop_grace_period: ${SUB2API_STOP_GRACE_PERIOD:-" stop "s}" {
        stop_count++
      }
      in_application && $0 == "      - SERVER_SHUTDOWN_TIMEOUT_SECONDS=${SERVER_SHUTDOWN_TIMEOUT_SECONDS:-}" {
        env_count++
      }
      $0 ~ /^[[:space:]]*#/ { next }
      $0 ~ /stop_grace_period:/ { any_stop++ }
      $0 ~ /SERVER_SHUTDOWN_TIMEOUT_SECONDS/ { any_env++ }
      END { printf "%d %d %d %d\n", stop_count, env_count, any_stop, any_env }
    '
  )
  [ "$counts" = "1 1 1 1" ] || \
    fail "$compose_file must give the sub2api service stop_grace_period \${SUB2API_STOP_GRACE_PERIOD:-${stop_grace}s} and pass SERVER_SHUTDOWN_TIMEOUT_SECONDS with an empty fallback exactly once (got $counts)"
done

apple_timeout=$(normalized deploy/apple-container.sh | sed -n 's/^CONTAINER_STOP_TIMEOUT_SECONDS=\([0-9][0-9]*\)$/\1/p')
[ -n "$apple_timeout" ] || fail "deploy/apple-container.sh must define CONTAINER_STOP_TIMEOUT_SECONDS"
[ "$apple_timeout" -ge $((default_grace + forced_and_cleanup_seconds)) ] || \
  fail "Apple container stop timeout ${apple_timeout}s is shorter than the default shutdown budget"
if normalized deploy/apple-container.sh | grep -Eq 'container stop --time [0-9]'; then
  fail "deploy/apple-container.sh must stop containers with CONTAINER_STOP_TIMEOUT_SECONDS"
fi

# systemd 默认 DefaultTimeoutStopSec=90；显式值不得低于它，且两份 unit 必须一致。
unit_timeout=""
for unit_source in deploy/sub2api.service deploy/install.sh; do
  timeouts=$(normalized "$unit_source" | sed -n 's/^TimeoutStopSec=\([0-9][0-9]*\)$/\1/p')
  [ "$(printf '%s\n' "$timeouts" | grep -c .)" -eq 1 ] || \
    fail "$unit_source must set TimeoutStopSec exactly once"
  [ "$timeouts" -ge 90 ] || fail "$unit_source lowers TimeoutStopSec below the systemd default of 90s"
  if [ -n "$unit_timeout" ] && [ "$unit_timeout" != "$timeouts" ]; then
    fail "deploy/sub2api.service and the install.sh unit disagree on TimeoutStopSec"
  fi
  unit_timeout=$timeouts
  # 默认 KillMode=control-group 会在排空开始时就 SIGTERM 插件子进程。
  kill_modes=$(normalized "$unit_source" | sed -n 's/^KillMode=//p')
  [ "$kill_modes" = "mixed" ] || \
    fail "$unit_source must set KillMode=mixed exactly once so SIGTERM reaches only the main process"
done

printf 'shutdown deployment test passed\n'
