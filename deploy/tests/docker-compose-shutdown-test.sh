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

# deploy/DOCKER.md 是镜像页说明，示例常被直接照抄：每条 docker run 都要带足关停预算的
# --stop-timeout 和 --restart unless-stopped（后台“重启”以退出码 0 退出，靠它拉起），
# 内联 compose 的 sub2api 服务同样要有 restart 与足够的 stop_grace_period。
# 只看代码块，续行（行尾反斜杠）拼成一条命令再判断。
doc_counts=$(
  normalized deploy/DOCKER.md | awk -v min="$((default_grace + forced_and_cleanup_seconds))" '
    /^```/ {
      in_code = !in_code
      cmd = ""
      in_application = 0
      next
    }
    !in_code { next }
    $0 == "  sub2api:" { in_application = 1 }
    in_application && $0 ~ /^  [A-Za-z0-9_-]+:$/ && $0 != "  sub2api:" { in_application = 0 }
    in_application && $0 == "    restart: unless-stopped" { compose_restart++ }
    in_application && $0 ~ /^    stop_grace_period: [0-9]+s$/ {
      value = $0
      sub(/^    stop_grace_period: /, "", value)
      sub(/s$/, "", value)
      if (value + 0 >= min) compose_stop++
    }
    {
      line = $0
      continued = sub(/\\$/, "", line)
      cmd = cmd " " line
      if (continued) next
      if (cmd ~ /docker run /) {
        runs++
        ok = (cmd ~ /--restart unless-stopped/)
        if (match(cmd, /--stop-timeout [0-9]+/)) {
          if (substr(cmd, RSTART + 15, RLENGTH - 15) + 0 < min) ok = 0
        } else {
          ok = 0
        }
        if (ok) good_runs++
      }
      cmd = ""
    }
    END { printf "%d %d %d %d\n", runs, good_runs, compose_restart, compose_stop }
  '
)
set -- $doc_counts
[ "$1" -ge 1 ] && [ "$1" = "$2" ] && [ "$3" -eq 1 ] && [ "$4" -eq 1 ] || \
  fail "deploy/DOCKER.md examples must give every docker run --restart unless-stopped and --stop-timeout >= $((default_grace + forced_and_cleanup_seconds)), and the inline compose sub2api service restart: unless-stopped and a long enough stop_grace_period (got runs/ok/restart/stop $doc_counts)"

# Apple container 的停止超时从 .env 解析（行为由 apple-container-test.sh 覆盖），
# 这里只检查默认值与随宽限增加的余量都覆盖关停预算。
apple_timeout=$(normalized deploy/apple-container.sh | sed -n 's/^DEFAULT_CONTAINER_STOP_TIMEOUT_SECONDS=\([0-9][0-9]*\)$/\1/p')
[ -n "$apple_timeout" ] || fail "deploy/apple-container.sh must define DEFAULT_CONTAINER_STOP_TIMEOUT_SECONDS"
[ "$apple_timeout" -ge $((default_grace + forced_and_cleanup_seconds)) ] || \
  fail "Apple container stop timeout ${apple_timeout}s is shorter than the default shutdown budget"
apple_margin=$(normalized deploy/apple-container.sh | sed -n 's/^CONTAINER_STOP_MARGIN_SECONDS=\([0-9][0-9]*\)$/\1/p')
[ -n "$apple_margin" ] || fail "deploy/apple-container.sh must define CONTAINER_STOP_MARGIN_SECONDS"
[ "$apple_margin" -ge "$forced_and_cleanup_seconds" ] || \
  fail "Apple container stop margin ${apple_margin}s does not cover the forced phase and cleanup"
[ "$(normalized deploy/.env.example | grep -Fxc 'APPLE_CONTAINER_STOP_TIMEOUT_SECONDS=' || true)" -eq 1 ] || \
  fail "deploy/.env.example must document APPLE_CONTAINER_STOP_TIMEOUT_SECONDS (empty) exactly once"
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
