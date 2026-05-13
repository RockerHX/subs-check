#!/bin/sh
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
CONFIG_PATH=${SUBS_CHECK_HOME_FILTER_CONFIG:-"$REPO_ROOT/config/config.yaml"}
LISTEN_ADDR=${SUBS_CHECK_HOME_FILTER_LISTEN:-"127.0.0.1:8399"}
STATE_DIR=${SUBS_CHECK_HOME_FILTER_STATE_DIR:-"$REPO_ROOT/output/home-filter-state"}
export SUBS_CHECK_HOME_FILTER_STATE_DIR="$STATE_DIR"

if [ -n "${SUBS_CHECK_HOME_FILTER_BIN:-}" ]; then
  exec "$SUBS_CHECK_HOME_FILTER_BIN" -config "$CONFIG_PATH" -serve "$LISTEN_ADDR" "$@"
fi

if command -v go >/dev/null 2>&1; then
  cd "$REPO_ROOT"
  exec go run ./tools/home-filter -config "$CONFIG_PATH" -serve "$LISTEN_ADDR" "$@"
fi

printf '%s\n' "home-filter service requires Go in PATH, or set SUBS_CHECK_HOME_FILTER_BIN to a prebuilt tools/home-filter binary." >&2
exit 127
