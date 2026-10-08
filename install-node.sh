#!/usr/bin/env bash

set -Eeuo pipefail

readonly SERVICE_BASE_PLACEHOLDER="__SPEEDQUALITY_""REPORT_BASE__"
readonly VERSION_PLACEHOLDER="__SPEEDQUALITY_""PROBE_VERSION__"

SERVICE_BASE="${SPEEDQUALITY_SERVICE_BASE:-__SPEEDQUALITY_REPORT_BASE__}"
NODE_VERSION="${SPEEDQUALITY_NODE_VERSION:-__SPEEDQUALITY_PROBE_VERSION__}"
INSTALL_ONLY=0
TEMP_DIR=""

usage() {
  cat <<'EOF'
SpeedQuality 节点安装器

用法:
  bash <(curl -fsSL https://<SpeedQuality 域名>/install-node)

选项:
      --install-only    只安装 sq-node，不进入首次设置
  -h, --help            显示帮助

默认流程:
  1. 识别 Linux amd64/arm64 并下载经过 SHA-256 校验的 sq-node
  2. 安装到 /usr/local/bin/sq-node
  3. 自动检测公网地址、地区和运营商，显示一次配置摘要
  4. 完成注册并安装 systemd 后台服务与签名更新定时器

安装完成后运行 sudo sq-node 可再次打开管理菜单。
EOF
}

die() {
  printf '[X] %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" &&
        "$(basename -- "$TEMP_DIR")" == speedquality-node.* ]]; then
    rm -rf -- "$TEMP_DIR"
  fi
}
trap cleanup EXIT

as_root() {
  if ((EUID == 0)); then
    "$@"
    return
  fi
  command -v sudo >/dev/null 2>&1 || die "安装到 /usr/local/bin 需要 root 或 sudo"
  sudo "$@"
}

while (($#)); do
  case "$1" in
    --install-only)
      INSTALL_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知参数: $1（使用 --help 查看帮助）"
      ;;
  esac
done

[[ "$(uname -s)" == "Linux" ]] || die "sq-node 目前只支持 Linux"
for command_name in curl sha256sum awk install; do
  command -v "$command_name" >/dev/null 2>&1 || die "缺少命令: $command_name"
done

if [[ "$SERVICE_BASE" == "$SERVICE_BASE_PLACEHOLDER" ||
      "$NODE_VERSION" == "$VERSION_PLACEHOLDER" ]]; then
  die "安装器尚未注入服务地址和版本，请从已部署的 SpeedQuality /install-node 获取"
fi
[[ "$SERVICE_BASE" =~ ^https://[^/]+$ ]] || die "SpeedQuality 服务地址无效"
[[ "$NODE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "sq-node 版本无效"

case "$(uname -m)" in
  x86_64|amd64) NODE_ARCH="amd64" ;;
  aarch64|arm64) NODE_ARCH="arm64" ;;
  *) die "不支持的 CPU 架构: $(uname -m)" ;;
esac

ASSET="sq-node-linux-${NODE_ARCH}"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/speedquality-node.XXXXXX")"

printf '[i] 正在下载 sq-node %s (%s)\n' "$NODE_VERSION" "$NODE_ARCH"
curl -fsSL --retry 2 --connect-timeout 10 \
  "$SERVICE_BASE/bin/$NODE_VERSION/$ASSET" -o "$TEMP_DIR/$ASSET"
curl -fsSL --retry 2 --connect-timeout 10 \
  "$SERVICE_BASE/bin/$NODE_VERSION/checksums.txt" -o "$TEMP_DIR/checksums.txt"

CHECKSUM_LINE="$(awk -v asset="$ASSET" '$2 == asset { print; found = 1 } END { if (!found) exit 1 }' \
  "$TEMP_DIR/checksums.txt")" || die "校验清单中没有 $ASSET"
(
  cd "$TEMP_DIR"
  printf '%s\n' "$CHECKSUM_LINE" | sha256sum -c -
) >/dev/null || die "sq-node SHA-256 校验失败"

as_root install -m 0755 "$TEMP_DIR/$ASSET" /usr/local/bin/sq-node
printf '[+] sq-node 已安装到 /usr/local/bin/sq-node\n'

if ((INSTALL_ONLY)); then
  printf '[i] 运行 sudo sq-node setup --platform %s 完成首次设置\n' "$SERVICE_BASE"
  exit 0
fi

if [[ ! -t 0 ]]; then
  printf '[!] 当前不是交互终端，已跳过首次设置。\n' >&2
  printf '[i] 请运行 sudo sq-node setup --platform %s\n' "$SERVICE_BASE"
  exit 0
fi

as_root /usr/local/bin/sq-node setup --platform "$SERVICE_BASE"
