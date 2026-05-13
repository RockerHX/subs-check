#!/bin/sh
set -eu

CONFIG_PATH=/app/config/config.yaml
prev=""
for arg in "$@"; do
  if [ "$prev" = "-f" ]; then
    CONFIG_PATH="$arg"
    prev=""
    continue
  fi
  case "$arg" in
    -f)
      prev="-f"
      ;;
    -f=*)
      CONFIG_PATH="${arg#-f=}"
      ;;
  esac
done

HOME_FILTER_ADDR="${SUBS_CHECK_HOME_FILTER_LISTEN:-127.0.0.1:8399}"
if [ -f "$CONFIG_PATH" ]; then
  /app/home-filter -config "$CONFIG_PATH" -serve "$HOME_FILTER_ADDR" &
fi
exec /app/subs-check "$@"
