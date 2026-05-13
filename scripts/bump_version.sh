#!/usr/bin/env bash
# bump_version.sh - 交互式创建发布 tag（不 push）

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

while :; do
  read -erp "请输入新版本号 (格式 X.Y.Z，例如 1.7.8，输入 q 退出): " NEW_VERSION_RAW

  if [[ "$NEW_VERSION_RAW" == "q" || "$NEW_VERSION_RAW" == "Q" ]]; then
    echo "已取消"
    exit 0
  fi

  CLEANED="$(printf '%s' "$NEW_VERSION_RAW" | tr -d '[:space:]')"
  CLEANED="${CLEANED//。/.}"
  CLEANED="$(printf '%s' "$CLEANED" | LC_ALL=C tr -cd '0-9.')"

  NEW_VERSION="$CLEANED"

  if printf '%s\n' "$NEW_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    break
  fi

  echo "Error: 版本号格式必须类似 1.7.8.0，请重新输入。"
done

TAG_NAME="v$NEW_VERSION"

if git rev-parse --verify --quiet "refs/tags/$TAG_NAME" >/dev/null; then
  echo "Error: tag '$TAG_NAME' 已存在。" >&2
  exit 1
fi

git tag "$TAG_NAME"

echo "已创建 tag: $TAG_NAME"
echo "请手动执行 push，例如："
echo "  git push origin $TAG_NAME"
