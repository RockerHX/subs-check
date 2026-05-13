#!/usr/bin/env bash
# bump_version.sh - 交互式记录版本、提交、创建并推送发布 tag

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Error: 工作区或暂存区有未提交改动，请先处理干净再运行。" >&2
  exit 1
fi

CURRENT_BRANCH="$(git branch --show-current)"
if [[ "$CURRENT_BRANCH" != "master" ]]; then
  echo "Error: 当前分支是 '$CURRENT_BRANCH'，请切换到 master 后再运行。" >&2
  exit 1
fi

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

  echo "Error: 版本号格式必须类似 1.7.8，请重新输入。"
done

TAG_NAME="v$NEW_VERSION"

if git rev-parse --verify --quiet "refs/tags/$TAG_NAME" >/dev/null; then
  echo "Error: tag '$TAG_NAME' 已存在。" >&2
  exit 1
fi

printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S %z')" "$TAG_NAME" >> release.log

git add release.log
git commit -m "release: $TAG_NAME"

git tag "$TAG_NAME"

echo "已记录 release.log 并提交: release: $TAG_NAME"
echo "已创建 tag: $TAG_NAME"
git push origin "$CURRENT_BRANCH"
git push origin "$TAG_NAME"

echo "已推送分支: $CURRENT_BRANCH"
echo "已推送 tag: $TAG_NAME"
