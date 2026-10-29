#!/usr/bin/env bash
# Checks the Grok Build client version the gateway advertises to
# cli-chat-proxy.grok.com (see .github/workflows/grok-cli-version-check.yml).
#
#   bash .github/scripts/grok-cli-version-check.sh            # also compare with https://x.ai/cli/stable
#   bash .github/scripts/grok-cli-version-check.sh --offline  # repository consistency only
#
# GROK_CLI_STABLE_VERSION replaces the live stable lookup (local runs and tests).
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
pin_file=backend/internal/pkg/xai/billing.go
floor_file=backend/internal/pkg/xai/cli_identity.go
readme=README.md
stable_url=${GROK_CLI_STABLE_URL:-https://x.ai/cli/stable}

offline=false
case "${1:-}" in
  "") ;;
  --offline) offline=true ;;
  *)
    echo "usage: $0 [--offline]" >&2
    exit 2
    ;;
esac

errors=0
error() {
  echo "::error::$*"
  errors=$((errors + 1))
}

# read_go_string_const NAME FILE prints the value of a `NAME = "value"` const,
# whatever alignment gofmt gives the declaration.
read_go_string_const() {
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\([^\"]*\)\".*$/\1/p" "$repo_root/$2" | head -n 1
}

# The proxy and https://x.ai/cli/stable only deal in plain releases.
is_release_version() {
  printf '%s' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'
}

# version_cmp A B prints -1, 0 or 1. Both arguments must be release versions.
version_cmp() {
  local IFS=.
  local -a left=($1) right=($2)
  local i
  for i in 0 1 2; do
    if ((10#${left[i]} < 10#${right[i]})); then
      echo -1
      return
    fi
    if ((10#${left[i]} > 10#${right[i]})); then
      echo 1
      return
    fi
  done
  echo 0
}

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$@" >>"$GITHUB_STEP_SUMMARY"
  fi
}

pinned=$(read_go_string_const CLIClientVersion "$pin_file")
floor=$(read_go_string_const CLIStableVersion "$floor_file")

if ! is_release_version "$pinned"; then
  error "CLIClientVersion in $pin_file is '$pinned'; expected a MAJOR.MINOR.PATCH release"
fi
if ! is_release_version "$floor"; then
  error "CLIStableVersion in $floor_file is '$floor'; expected a MAJOR.MINOR.PATCH release"
fi
if [ "$errors" -gt 0 ]; then
  exit 1
fi

if [ "$(version_cmp "$pinned" "$floor")" = -1 ]; then
  error "CLIClientVersion $pinned is below the proxy floor CLIStableVersion $floor; every OAuth inference request would get HTTP 426"
fi
# The README's XAI_GROK_CLI_VERSION row tells operators which version the
# binary pins and which floor overrides must clear.
if ! grep -Fq "$(printf 'pins Grok Build `%s`' "$pinned")" "$repo_root/$readme"; then
  error "$readme does not advertise the pinned Grok Build version $pinned; update the XAI_GROK_CLI_VERSION row"
fi
if ! grep -Fq "$(printf 'floor (`%s`)' "$floor")" "$repo_root/$readme"; then
  error "$readme does not advertise the proxy floor $floor; update the XAI_GROK_CLI_VERSION row"
fi
echo "pinned=$pinned floor=$floor"

stable=""
status=""
if [ "$offline" = false ]; then
  if [ -n "${GROK_CLI_STABLE_VERSION:-}" ]; then
    stable=$GROK_CLI_STABLE_VERSION
  elif ! stable=$(curl -fsS --retry 3 --retry-all-errors --retry-delay 5 --max-time 20 "$stable_url"); then
    stable=""
  fi
  stable=$(printf '%s' "$stable" | tr -d '[:space:]')
  echo "stable=$stable"

  if [ -z "$stable" ]; then
    error "could not read the stable Grok CLI version from $stable_url"
  elif ! is_release_version "$stable"; then
    error "$stable_url returned '$stable'; expected a MAJOR.MINOR.PATCH release"
  else
    case "$(version_cmp "$pinned" "$stable")" in
      -1)
        status="behind"
        error "Grok CLI pin $pinned is behind stable $stable; bump CLIClientVersion in $pin_file and the $readme row (operators can set XAI_GROK_CLI_VERSION=$stable until a release ships)"
        ;;
      1)
        status="ahead"
        echo "::warning::Grok CLI pin $pinned is ahead of stable $stable; keep it only if that build's traffic was captured"
        ;;
      *)
        status="current"
        ;;
    esac
  fi
fi

summary "### Grok CLI version" "" \
  "| | Version |" "|---|---|" \
  "| Pinned \`CLIClientVersion\` | \`$pinned\` |" \
  "| Proxy floor \`CLIStableVersion\` | \`$floor\` |"
if [ "$offline" = false ]; then
  summary "| Published stable | \`${stable:-unavailable}\` |" "" "Pin status: **${status:-unknown}**"
fi

if [ "$errors" -gt 0 ]; then
  exit 1
fi
