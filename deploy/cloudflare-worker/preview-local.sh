#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
VENV_DIR="${SPEEDQUALITY_PREVIEW_VENV:-$SCRIPT_DIR/.venv}"
VENV_PYTHON="$VENV_DIR/bin/python"
PREVIEW_HOST="${HOST:-127.0.0.1}"
PREVIEW_PORT="${PORT:-4173}"
BUILD_ONLY=0

usage() {
  cat <<'EOF'
用法: bash preview-local.sh [--build-only]

  --build-only  在虚拟环境中生成静态页面，但不启动 HTTP 服务

环境变量:
  HOST  监听地址，默认 127.0.0.1
  PORT  监听端口，默认 4173
  SPEEDQUALITY_PREVIEW_SNAPSHOT  可选的 NodeQuality 安全快照 JSON
EOF
}

case "${1:-}" in
  "") ;;
  --build-only) BUILD_ONLY=1 ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

if [[ ! "$PREVIEW_PORT" =~ ^[0-9]+$ ]]; then
  printf '无效端口: %s\n' "$PREVIEW_PORT" >&2
  exit 2
fi
if ((10#$PREVIEW_PORT < 1 || 10#$PREVIEW_PORT > 65535)); then
  printf '无效端口: %s\n' "$PREVIEW_PORT" >&2
  exit 2
fi

command -v python3 >/dev/null 2>&1 || {
  printf '缺少 Python 3，无法创建本地虚拟环境。\n' >&2
  exit 1
}
command -v npm >/dev/null 2>&1 || {
  printf '缺少 npm，无法生成预览页面。\n' >&2
  exit 1
}

if [[ ! -x "$VENV_PYTHON" ]]; then
  printf '[i] 正在创建 Python 虚拟环境: %s\n' "$VENV_DIR"
  if ! python3 -m venv --without-pip "$VENV_DIR"; then
    printf '创建虚拟环境失败，请检查当前 Python 3 的 venv 支持。\n' >&2
    exit 1
  fi
fi

cd "$SCRIPT_DIR"
npm run preview:build

if ((BUILD_ONLY == 1)); then
  printf '[+] 预览已生成: %s/preview/report.html\n' "$SCRIPT_DIR"
  exit 0
fi

printf '\n[i] 正在启动本地预览\n'
printf '    全部:         http://%s:%s/report.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    基本信息:     http://%s:%s/report-basic.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    IP 质量:      http://%s:%s/report-ip-quality.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    网络质量:     http://%s:%s/report-network-quality.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    回程路由:     http://%s:%s/report-route.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    联合报告 SQ:  http://%s:%s/report-speed.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    时间过旧示例: http://%s:%s/report-stale-speed.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    时间未知示例: http://%s:%s/report-time-unknown-speed.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    独立 SQ:      http://%s:%s/report-standalone.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '\n    NQ 校验结果预览:\n'
printf '    完整 IP 一致: http://%s:%s/validation-full-ip-ok.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    脱敏网段一致: http://%s:%s/validation-masked-ip-ok.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    时间差过大:   http://%s:%s/validation-time-stale.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    时间无法确认: http://%s:%s/validation-time-unknown.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    身份不匹配:   http://%s:%s/validation-identity-mismatch.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    NQ 无法校验:  http://%s:%s/validation-report-unavailable.html\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
printf '    按 Ctrl+C 停止。\n\n'

exec "$VENV_PYTHON" -m http.server "$PREVIEW_PORT" \
  --bind "$PREVIEW_HOST" \
  --directory "$SCRIPT_DIR/preview"
