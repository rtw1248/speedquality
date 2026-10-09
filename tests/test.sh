#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
RUNNER="$ROOT_DIR/run.sh"
MOCK_PROBE="$ROOT_DIR/tests/fixtures/mock-probe.sh"
FIXTURES="$ROOT_DIR/tests/fixtures"
LEASE_DIR="$ROOT_DIR/tests/fixtures/leases"
GEO_HUBEI="$ROOT_DIR/tests/fixtures/geo-hubei.json"
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/speedquality-test.XXXXXX")
REAL_PROBE="$TEST_DIR/sqprobe"
TESTS=0
REPORT_TOKEN="IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB"
REPORT_URL="https://nodequality.com/r/$REPORT_TOKEN"
REPORT_BASE="https://reports.example"
REPORT_PAGE="$REPORT_BASE/r/AbCdEfGhIjKl"
REPORT_RESPONSE="$TEST_DIR/report-response.txt"
CURRENT_INFO="$TEST_DIR/current-ipinfo.json"
MATCH_RECORD="$TEST_DIR/match-record.json"
MISMATCH_RECORD="$TEST_DIR/mismatch-record.json"
BROAD_MASK_RECORD="$TEST_DIR/broad-mask-record.json"
STALE_RECORD="$TEST_DIR/stale-record.json"
TIMELESS_RECORD="$TEST_DIR/timeless-record.json"
LATEST_RECORD="$TEST_DIR/latest-record.json"
MARKDOWN_TIME_RECORD="$TEST_DIR/markdown-time-record.json"
TOO_MANY_RECORD="$TEST_DIR/too-many-record.json"
FOREIGN_GEO="$TEST_DIR/geo-foreign.json"
MOCK_CURL="$ROOT_DIR/tests/fixtures/mock-curl.sh"

cleanup() {
  if [[ "${KEEP_TEST_DIR:-0}" == "1" ]]; then
    printf '保留测试目录: %s\n' "$TEST_DIR" >&2
    return
  fi
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT

fail() {
  printf 'not ok - %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "$expected" "$file" || fail "$file 不包含: $expected"
}

assert_not_contains() {
  local file="$1"
  local unexpected="$2"
  if grep -Fq -- "$unexpected" "$file"; then
    fail "$file 不应包含: $unexpected"
  fi
}

assert_line() {
  local file="$1"
  local expected="$2"
  grep -Fxq -- "$expected" "$file" || fail "$file 没有完整参数行: $expected"
}

pass() {
  TESTS=$((TESTS + 1))
  printf 'ok %d - %s\n' "$TESTS" "$1"
}

make_nodequality_record() {
  local destination="$1"
  local report_ip="$2"
  local report_asn="$3"
  local header_time="$4"
  local latest_log_time="${5:-}"
  local json_time="${6:-}"
  local markdown_time="${7:-}"
  python3 - "$destination" "$report_ip" "$report_asn" \
    "$header_time" "$latest_log_time" "$json_time" "$markdown_time" <<'PY'
import base64
import io
import json
import sys
import zipfile

(
    destination, report_ip, report_asn, header_time,
    latest_log_time, json_time, markdown_time,
) = sys.argv[1:8]
archive = io.BytesIO()
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as output:
    header = "\x1b[0;36mNodeQuality fixture\n"
    if header_time:
        header += f"报告时间：{header_time}  脚本版本：test\n"
    header += "\x1b[0m"
    output.writestr("header_info.log", header)
    output.writestr(
        "ip_quality.log",
        "########################################################################\n"
        f"IP质量体检报告：{report_ip}\n"
        f"自治系统号：AS{report_asn}\n",
    )
    output.writestr(
        "checks/unsafe.log",
        "\x1b]0;unsafe title\x07\x1b[31mcolored\x1b[0m\x01\n"
        "<script>alert('snapshot')</script>\n",
    )
    markdown_time_line = (
        f"报告时间：{markdown_time}  脚本版本：test\r\n"
        if markdown_time else ""
    )
    markdown = (
        ":::: tabs\r\n"
        "::: tab-item ??基本信息\r\n\r\n"
        "```ansi\r\n"
        "  保留  对齐空格\r\n"
        "\x1b[31m红色标题\x1b[0m\r\n"
        + markdown_time_line
        + "\x1b]0;unsafe title\x07控制符后文本\x01\r\n"
        "<script>alert('snapshot')</script>\r\n"
        "```\r\n\r\n"
        ":::\r\n"
        "::: tab-item ??IP质量\r\n\r\n"
        "```ansi\r\n"
        f"IP质量体检报告：{report_ip}\r\n"
        f"自治系统号：AS{report_asn}\r\n"
        "```\r\n\r\n"
        ":::\r\n"
        "::: tab-item ??网络质量\r\n"
        "![image](https://images.example/network.webp)\r\n\r\n"
        ":::\r\n"
        "::: tab-item ??回程路由\r\n"
        "![image](https://images.example/route.png)\r\n\r\n"
        ":::\r\n"
        "::::\r\n"
    )
    output.writestr("nodequality.md", markdown.encode("gb18030"))
    output.writestr("notes.txt", "this file type must not be included")
    output.writestr("../escape.log", "this unsafe path must not be included")
    if latest_log_time:
        output.writestr("checks/network.log", f"检测时间：{latest_log_time}\n")
    if json_time:
        output.writestr("details.json", json.dumps({"Head": {"Time": json_time}}))
payload = {
    "success": True,
    "data": {
        "token": "IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
        "asn": report_asn,
        "asOrganization": "Fixture Network",
        "location": {"city": "Hong Kong"},
        "result": base64.b64encode(archive.getvalue()).decode("ascii"),
        "provider": "fixture",
    },
}
with open(destination, "w", encoding="utf-8") as handle:
    json.dump(payload, handle, separators=(",", ":"))
PY
}

make_too_many_nodequality_record() {
  local destination="$1"
  python3 - "$destination" <<'PY'
import base64
import io
import json
import sys
import zipfile

archive = io.BytesIO()
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as output:
    output.writestr("ip_quality.log", "IP质量体检报告：45.78.*.*\n")
    for index in range(256):
        output.writestr(f"checks/member-{index:03d}.log", "fixture\n")
payload = {
    "success": True,
    "data": {
        "asn": "25820",
        "result": base64.b64encode(archive.getvalue()).decode("ascii"),
    },
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(payload, handle, separators=(",", ":"))
PY
}

prepare_fixtures() {
  local close_time stale_time
  close_time=$(TZ=Asia/Shanghai date -d '5 minutes ago' '+%Y-%m-%d %H:%M:%S CST')
  stale_time=$(TZ=Asia/Shanghai date -d '70 minutes ago' '+%Y-%m-%d %H:%M:%S CST')
  printf '%s\n' '{"ip":"45.78.123.4","asn":25820,"asOrganization":"Fixture Network","location":{"city":"Hong Kong"},"ts":1790384524406}' > "$CURRENT_INFO"
  printf '%s\n' "$REPORT_PAGE" > "$REPORT_RESPONSE"
  printf '%s\n' '{"country_code":"US","region_code":"HB","region":"Foreign HB"}' > "$FOREIGN_GEO"
  make_nodequality_record "$MATCH_RECORD" '45.78.*.*' '25820' "$close_time"
  make_nodequality_record "$MISMATCH_RECORD" '103.1.*.*' '9999' "$close_time"
  make_nodequality_record "$BROAD_MASK_RECORD" '45.*.*.*' '25820' "$close_time"
  make_nodequality_record "$STALE_RECORD" '45.78.*.*' '25820' "$stale_time"
  make_nodequality_record "$TIMELESS_RECORD" '45.78.*.*' '25820' ''
  make_nodequality_record "$LATEST_RECORD" '45.78.*.*' '25820' \
    '2020-01-01 00:00:00 CST' "$close_time" '2021-01-01 00:00:00 CST'
  make_nodequality_record "$MARKDOWN_TIME_RECORD" '45.78.*.*' '25820' \
    '' '' '' "$close_time"
  make_too_many_nodequality_record "$TOO_MANY_RECORD"
}

report_env() {
  env SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_REPORT_RESPONSE_FILE="$REPORT_RESPONSE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_NQ_VERIFY_BIN="$REAL_PROBE" \
    SPEEDQUALITY_SESSION_TOKEN="abcdefghijklmnopqrstuvwxyzABCDEFGH12345678" \
    SPEEDQUALITY_LEASE_DIR="$LEASE_DIR" \
    SPEEDQUALITY_HAS_IPV4=1 \
    SPEEDQUALITY_HAS_IPV6=0 "$@"
}

test_short_province_and_default_speed() {
  local args="$TEST_DIR/p-args.txt"
  local output="$TEST_DIR/p.out"
  report_env MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb >"$output" 2>&1

  assert_line "$args" 'hb v4'
  assert_contains "$output" '单线程；速度档位: 200 Mbps'
  assert_contains "$output" '1.05 GB'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  assert_not_contains "$output" '展示链接:'
  pass '-p 是 --province 的短写；仅 IPv4 可用时默认测试 IPv4'
}

test_auto_ssh_region() {
  local args="$TEST_DIR/auto-args.txt"
  local output="$TEST_DIR/auto.out"
  report_env \
    SPEEDQUALITY_SSH_CLIENT_IP=1.2.3.4 \
    SPEEDQUALITY_GEO_RESPONSE_FILE="$GEO_HUBEI" MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" >"$output" 2>&1

  assert_contains "$output" '自动选择 湖北 (hb)'
  assert_line "$args" 'hb v4'
  assert_contains "$output" '速度档位: 200 Mbps'
  pass '根据 SSH 来源自动选择省份'
}

test_speed_aliases_and_validation() {
  local speed args output
  for speed in 100 200 400; do
    args="$TEST_DIR/speed-$speed.txt"
    output="$TEST_DIR/speed-$speed.out"
    report_env MOCK_ARGS_FILE="$args" MOCK_TARGET_MBPS="$speed" \
      bash "$RUNNER" -p hb -s "$speed" >"$output" 2>&1
    assert_line "$args" 'hb v4'
    assert_contains "$output" "速度档位: $speed Mbps"
  done
  if report_env \
    bash "$RUNNER" -p hb --speed 300 >"$TEST_DIR/speed-invalid.out" 2>&1; then
    fail '不支持的测速档位被接受'
  fi
  assert_contains "$TEST_DIR/speed-invalid.out" '只支持 100、200 或 400 Mbps'
  if report_env bash "$RUNNER" -p hb --mode s >"$TEST_DIR/mode-removed.out" 2>&1; then
    fail '已移除的 --mode 被接受'
  fi
  assert_contains "$TEST_DIR/mode-removed.out" '参数 --mode 已移除'
  pass '-s/--speed 支持三个档位并拒绝旧 --mode'
}

test_ip_family_selection() {
  local args="$TEST_DIR/auto-dual-args.txt"
  report_env SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=1 MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb >"$TEST_DIR/auto-dual.out" 2>&1
  assert_line "$args" 'hb v4'
  assert_line "$args" 'hb v6'
  assert_contains "$TEST_DIR/auto-dual.out" 'IP: v4 + v6'
  assert_not_contains "$TEST_DIR/auto-dual.out" '默认测试两者'

  args="$TEST_DIR/v4-only-args.txt"
  report_env SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=1 MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb -v4 >"$TEST_DIR/v4-only.out" 2>&1
  assert_line "$args" 'hb v4'
  assert_not_contains "$args" 'hb v6'
  assert_contains "$TEST_DIR/v4-only.out" 'IP: v4'
  assert_not_contains "$TEST_DIR/v4-only.out" '已指定仅测试 IPv4'

  args="$TEST_DIR/v6-only-args.txt"
  report_env SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=1 MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb --ipv6 >"$TEST_DIR/v6-only.out" 2>&1
  assert_not_contains "$args" 'hb v4'
  assert_line "$args" 'hb v6'
  assert_contains "$TEST_DIR/v6-only.out" 'IP: v6'
  assert_not_contains "$TEST_DIR/v6-only.out" '已指定仅测试 IPv6'

  if report_env SPEEDQUALITY_HAS_IPV4=0 SPEEDQUALITY_HAS_IPV6=1 \
    bash "$RUNNER" -p hb -v4 >"$TEST_DIR/v4-unavailable.out" 2>&1; then
    fail '-v4 在 IPv4 不可用时静默继续'
  fi
  assert_contains "$TEST_DIR/v4-unavailable.out" '未检测到 IPv4 连通性'

  if report_env SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=0 \
    bash "$RUNNER" -p hb -v6 >"$TEST_DIR/v6-unavailable.out" 2>&1; then
    fail '-v6 在 IPv6 不可用时静默继续'
  fi
  assert_contains "$TEST_DIR/v6-unavailable.out" '未检测到 IPv6 连通性'

  if report_env bash "$RUNNER" -p hb -v4 -v6 >"$TEST_DIR/ip-conflict.out" 2>&1; then
    fail '-v4 和 -v6 被同时接受'
  fi
  assert_contains "$TEST_DIR/ip-conflict.out" '不能同时使用'

  if report_env SPEEDQUALITY_HAS_IPV4=0 SPEEDQUALITY_HAS_IPV6=0 \
    bash "$RUNNER" -p hb >"$TEST_DIR/no-ip.out" 2>&1; then
    fail '没有可用地址族时仍开始测速'
  fi
  assert_contains "$TEST_DIR/no-ip.out" '未检测到 IPv4 或 IPv6 连通性'
  pass '默认自动测试可用地址族，-v4/-v6 仅测指定类型且互斥'
}

test_default_dual_uses_family_bound_sessions() {
  local mock_bin="$TEST_DIR/mock-platform-bin"
  local log="$TEST_DIR/platform-family.log"
  local args="$TEST_DIR/platform-family-args.txt"
  local output="$TEST_DIR/platform-family.out"
  mkdir -p "$mock_bin"
  ln -s "$FIXTURES/mock-platform-curl.sh" "$mock_bin/curl"

  env PATH="$mock_bin:$PATH" \
    SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_REPORT_RESPONSE_FILE="$REPORT_RESPONSE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_HAS_IPV4=1 \
    SPEEDQUALITY_HAS_IPV6=1 \
    MOCK_PLATFORM_LOG="$log" \
    MOCK_PLATFORM_LEASE_DIR="$LEASE_DIR" \
    MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb >"$output" 2>&1

  assert_line "$log" 'session -4'
  assert_line "$log" 'session -6'
  assert_line "$log" 'lease v4 -4 Bearer abcdefghijklmnopqrstuvwxyzABCDEFGH12345678'
  assert_line "$log" 'lease v6 -6 Bearer ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210v6token'
  assert_line "$args" 'hb v4'
  assert_line "$args" 'hb v6'
  pass '双栈测速分别用 IPv4 和 IPv6 来源绑定会话申请租约'
}

test_ipv6_only_uses_v6_control_plane() {
  local mock_bin="$TEST_DIR/mock-v6-platform-bin"
  local log="$TEST_DIR/platform-v6-only.log"
  local args="$TEST_DIR/platform-v6-only-args.txt"
  local output="$TEST_DIR/platform-v6-only.out"
  mkdir -p "$mock_bin"
  ln -s "$FIXTURES/mock-platform-curl.sh" "$mock_bin/curl"

  env PATH="$mock_bin:$PATH" \
    SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_HAS_IPV4=0 \
    SPEEDQUALITY_HAS_IPV6=1 \
    MOCK_PLATFORM_LOG="$log" \
    MOCK_PLATFORM_LEASE_DIR="$LEASE_DIR" \
    MOCK_PLATFORM_REPORT_URL="$REPORT_PAGE" \
    MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p hb -v6 >"$output" 2>&1

  assert_not_contains "$log" 'session -4'
  assert_line "$log" 'session -6'
  assert_not_contains "$log" 'lease v4'
  assert_line "$log" 'lease v6 -6 Bearer ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210v6token'
  assert_line "$log" 'report -6 Bearer ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210v6token'
  assert_not_contains "$args" 'hb v4'
  assert_line "$args" 'hb v6'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass '仅 IPv6 测速的会话、租约和报告上传全部使用 IPv6'
}

test_node_directory_error_is_explained_without_retry() {
  local mock_bin="$TEST_DIR/mock-empty-directory-bin"
  local log="$TEST_DIR/empty-directory.log"
  local output="$TEST_DIR/empty-directory.out"
  mkdir -p "$mock_bin"
  ln -s "$FIXTURES/mock-platform-curl.sh" "$mock_bin/curl"

  if env PATH="$mock_bin:$PATH" \
    SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_HAS_IPV4=1 \
    SPEEDQUALITY_HAS_IPV6=0 \
    MOCK_PLATFORM_LOG="$log" \
    MOCK_PLATFORM_LEASE_DIR="$LEASE_DIR" \
    MOCK_PLATFORM_LEASE_ERROR="node_directory_unavailable" \
    bash "$RUNNER" -p hb >"$output" 2>&1; then
    fail '节点目录为空时测速脚本仍成功退出'
  fi

  assert_contains "$output" '湖北/v4 当前地区暂无可用测速节点（node_directory_unavailable）'
  assert_contains "$output" '请根据上方节点提示调整地区、IP 类型或稍后重试'
  [[ "$(grep -Fc 'lease v4 ' "$log")" == "1" ]] \
    || fail '节点目录为空时不应立即重试'
  assert_not_contains "$output" '将重试节点调度'
  pass '节点目录为空时显示明确原因且不做无效重试'
}

test_platform_clock_skew() {
  local mock_bin="$TEST_DIR/mock-clock-bin"
  local log="$TEST_DIR/platform-clock.log"
  local output="$TEST_DIR/platform-clock.out"
  local platform_epoch offset reference_epoch report_epoch latest_epoch start_uptime end_uptime
  local reference_times="$TEST_DIR/reference-times.txt"
  local report_time="$TEST_DIR/report-time.txt"
  mkdir -p "$mock_bin"
  ln -s "$FIXTURES/mock-platform-curl.sh" "$mock_bin/curl"
  for offset in 300 301 900 -900; do
    : > "$log"
    : > "$reference_times"
    platform_epoch=$(($(date +%s) + offset))
    read -r start_uptime _ < /proc/uptime
    env PATH="$mock_bin:$PATH" \
      SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
      SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
      SPEEDQUALITY_HAS_IPV4=1 \
      SPEEDQUALITY_HAS_IPV6=0 \
      MOCK_PLATFORM_LOG="$log" \
      MOCK_PLATFORM_LEASE_DIR="$LEASE_DIR" \
      MOCK_PLATFORM_EPOCH="$platform_epoch" \
      MOCK_REFERENCE_TIMES_FILE="$reference_times" \
      MOCK_PLATFORM_REPORT_TIME_FILE="$report_time" \
      bash "$RUNNER" -p hb >"$output" 2>&1

    assert_line "$log" 'time -4'
    assert_line "$log" 'session -4'
    assert_contains "$output" '分享报告:'
    assert_not_contains "$output" '系统时间'
    reference_epoch=$(<"$reference_times")
    report_epoch=$(<"$report_time")
    read -r end_uptime _ < /proc/uptime
    latest_epoch=$((platform_epoch + ${end_uptime%%.*} - ${start_uptime%%.*} + 1))
    [[ "$reference_epoch" =~ ^[0-9]{10}$ && "$report_epoch" =~ ^[0-9]{10}$ ]] \
      || fail '探测器或报告没有收到平台参考时间'
    ((reference_epoch >= platform_epoch && reference_epoch <= latest_epoch)) \
      || fail "探测器未使用平台参考时间：$reference_epoch，不在 $platform_epoch 到 $latest_epoch 之间"
    ((report_epoch >= reference_epoch && report_epoch <= latest_epoch)) \
      || fail '报告未使用平台参考时间'
  done
  pass '本机时间偏快或偏慢时静默继续，租约和报告采用平台参考时间'
}

test_bsg_preset_and_province_limit() {
  local args="$TEST_DIR/bsg-args.txt"
  local foreign_args="$TEST_DIR/bsg-foreign-args.txt"
  local output="$TEST_DIR/bsg.out"
  report_env \
    SPEEDQUALITY_SSH_CLIENT_IP=1.2.3.4 SPEEDQUALITY_GEO_RESPONSE_FILE="$GEO_HUBEI" \
    MOCK_ARGS_FILE="$args" MOCK_TARGET_MBPS=100 \
    bash "$RUNNER" -p bsg -s 100 >"$output" 2>&1

  assert_line "$args" 'hb v4'
  assert_line "$args" 'bj v4'
  assert_line "$args" 'sh v4'
  assert_line "$args" 'gd v4'
  assert_contains "$output" 'bsg 将选择 湖北、北京、上海和广东并自动去重'
  assert_contains "$output" '测速省份: 湖北,北京,上海,广东'
  assert_contains "$output" '预计最多约 2.10 GB'

  report_env \
    SPEEDQUALITY_SSH_CLIENT_IP=8.8.8.8 SPEEDQUALITY_GEO_RESPONSE_FILE="$FOREIGN_GEO" \
    MOCK_ARGS_FILE="$foreign_args" MOCK_TARGET_MBPS=100 \
    bash "$RUNNER" -p bsg -s 100 >"$TEST_DIR/bsg-foreign.out" 2>&1
  assert_not_contains "$foreign_args" 'hb v4'
  assert_line "$foreign_args" 'bj v4'
  assert_line "$foreign_args" 'sh v4'
  assert_line "$foreign_args" 'gd v4'
  assert_contains "$TEST_DIR/bsg-foreign.out" 'bsg 将只选择北京、上海和广东'
  assert_not_contains "$TEST_DIR/bsg-foreign.out" '请手动选择'

  if report_env bash "$RUNNER" -p all >"$TEST_DIR/all-removed.out" 2>&1; then
    fail '已关闭的 all 仍可使用'
  fi
  assert_contains "$TEST_DIR/all-removed.out" '全国 all 测试已关闭'

  args="$TEST_DIR/five-provinces-args.txt"
  report_env MOCK_ARGS_FILE="$args" MOCK_TARGET_MBPS=100 \
    bash "$RUNNER" -p hb,bj,sh,gd,js -s 100 >"$TEST_DIR/five-provinces.out" 2>&1
  assert_line "$args" 'js v4'
  assert_contains "$TEST_DIR/five-provinces.out" '测速省份: 湖北,北京,上海,广东,江苏'
  assert_contains "$TEST_DIR/five-provinces.out" '预计最多约 2.62 GB'

  if report_env bash "$RUNNER" -p hb,bj,sh,gd,js,zj >"$TEST_DIR/six-provinces.out" 2>&1; then
    fail '单次六省测速被接受'
  fi
  assert_contains "$TEST_DIR/six-provinces.out" '单次最多测试 5 个省份'
  pass 'bsg 优先使用来源省份加北上广，来源未知时退化为北上广三省'
}

test_exact_community_node_route() {
  local route_key="sqn_rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"
  local args="$TEST_DIR/node-route-args.txt"
  local output="$TEST_DIR/node-route.out"
  report_env \
    SPEEDQUALITY_NODE_ROUTE_FILE="$FIXTURES/node-route-hb-v4.json" \
    MOCK_ARGS_FILE="$args" MOCK_TARGET_MBPS=100 \
    bash "$RUNNER" --node "$route_key" -s 100 >"$output" 2>&1

  assert_line "$args" 'hb v4'
  assert_contains "$output" '已自动采用该省份'
  assert_contains "$output" '测速省份: 湖北'
  assert_contains "$output" '预计最多约 175.00 MB'
  assert_contains "$output" '指定 SQ 节点: ct / private；最高 200 Mbps'

  if report_env SPEEDQUALITY_NODE_ROUTE_FILE="$FIXTURES/node-route-hb-v4.json" \
    bash "$RUNNER" --node "$route_key" -p bj >"$TEST_DIR/node-wrong-region.out" 2>&1; then
    fail '--node 接受了与登记省份不一致的 -p'
  fi
  assert_contains "$TEST_DIR/node-wrong-region.out" '登记在 湖北 (hb)'

  if report_env SPEEDQUALITY_NODE_ROUTE_FILE="$FIXTURES/node-route-hb-v4.json" \
    bash "$RUNNER" --node "$route_key" -s 400 >"$TEST_DIR/node-too-fast.out" 2>&1; then
    fail '--node 接受了超过节点能力的档位'
  fi
  assert_contains "$TEST_DIR/node-too-fast.out" '最高支持 200 Mbps'

  if report_env SPEEDQUALITY_NODE_ROUTE_FILE="$FIXTURES/node-route-hb-v4.json" \
    SPEEDQUALITY_HAS_IPV6=1 bash "$RUNNER" --node "$route_key" -v6 \
    >"$TEST_DIR/node-no-v6.out" 2>&1; then
    fail '--node 在节点没有 IPv6 时接受了 -v6'
  fi
  assert_contains "$TEST_DIR/node-no-v6.out" '指定节点没有登记 IPv6'

  args="$TEST_DIR/node-dual-args.txt"
  report_env SPEEDQUALITY_NODE_ROUTE_FILE="$FIXTURES/node-route-hb-dual.json" \
    SPEEDQUALITY_HAS_IPV6=1 MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" --node "$route_key" >"$TEST_DIR/node-dual.out" 2>&1
  assert_line "$args" 'hb v4'
  assert_line "$args" 'hb v6'

  if report_env bash "$RUNNER" --node bad >"$TEST_DIR/node-invalid.out" 2>&1; then
    fail '无效 Route Key 被接受'
  fi
  assert_contains "$TEST_DIR/node-invalid.out" 'Route Key 格式无效'
  pass '--node 精确采用登记省份并校验档位、双栈和单节点流量'
}

test_traffic_estimate_and_measurement() {
  local netdev="$TEST_DIR/netdev"
  local output="$TEST_DIR/traffic.out"
  local mock_bin="$TEST_DIR/mock-download-bin"
  local checksums="$TEST_DIR/mock-checksums.txt"
  local platform_log="$TEST_DIR/download-platform.log"
  local probe_sha
  mkdir -p "$mock_bin"
  ln -s "$FIXTURES/mock-platform-curl.sh" "$mock_bin/curl"
  probe_sha=$(sha256sum "$MOCK_PROBE" | awk '{print $1}')
  printf '%s  sqprobe-linux-amd64\n%s  sqprobe-linux-arm64\n' \
    "$probe_sha" "$probe_sha" > "$checksums"
  printf '%s\n' \
    'Inter-|   Receive                                                |  Transmit' \
    ' face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed' \
    '  eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0' \
    > "$netdev"

  env PATH="$mock_bin:$PATH" \
    SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_REPORT_RESPONSE_FILE="$REPORT_RESPONSE" \
    SPEEDQUALITY_PROBE_BASE="$REPORT_BASE/bin/v1.0.11" \
    SPEEDQUALITY_CACHE_DIR="$TEST_DIR/download-cache" \
    SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=0 \
    MOCK_PLATFORM_LOG="$platform_log" MOCK_PLATFORM_LEASE_DIR="$LEASE_DIR" \
    MOCK_PLATFORM_CHECKSUMS_FILE="$checksums" MOCK_PLATFORM_PROBE_SOURCE="$MOCK_PROBE" \
    MOCK_DOWNLOAD_NETDEV_FILE="$netdev" \
    SPEEDQUALITY_NETDEV_FILE="$netdev" SPEEDQUALITY_NETWORK_INTERFACES=eth0 \
    MOCK_NETDEV_FILE="$netdev" MOCK_NETDEV_AFTER_RX=100001000 \
    MOCK_NETDEV_AFTER_TX=50002000 MOCK_TARGET_MBPS=100 \
    bash "$RUNNER" -p hb -s 100 >"$output" 2>&1

  assert_contains "$output" '预计最多约 525.00 MB'
  assert_not_contains "$output" '下载 SpeedQuality 探测器'
  assert_contains "$output" '实际流量: 下载 100.00 MB，上传 50.00 MB，合计 150.00 MB'
  [[ "$(grep -Fc '实际流量:' "$output")" -eq 1 ]] || fail '实际流量被重复输出'
  assert_not_contains "$output" '统计接口:'
  assert_not_contains "$output" '本次 SpeedQuality 执行期间网卡差值'
  assert_not_contains "$output" '可能包含同期其它进程流量'
  assert_not_contains "$output" '测试完成'
  assert_not_contains "$output" '展示类型:'
  assert_not_contains "$output" '展示链接:'
  pass '运行前估算流量，并把首次下载探测器计入实际流量'
}

test_chinese_provinces_and_city_rejection() {
  local args="$TEST_DIR/chinese-args.txt"
  local region
  report_env MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" -p '湖北，北京' >"$TEST_DIR/chinese.out" 2>&1
  assert_line "$args" 'hb v4'
  assert_line "$args" 'bj v4'

  if report_env bash "$RUNNER" -p 武汉 >"$TEST_DIR/city.out" 2>&1; then
    fail '城市名称被静默转换为省份'
  fi
  assert_contains "$TEST_DIR/city.out" "'武汉' 是城市名称"
  assert_contains "$TEST_DIR/city.out" '-p hb 或 -p 湖北'

  bash "$RUNNER" --list-provinces >"$TEST_DIR/provinces.out" 2>&1
  assert_contains "$TEST_DIR/provinces.out" 'hb 湖北'
  for region in 'tw 台湾' 'hk 香港' 'mo 澳门'; do
    assert_not_contains "$TEST_DIR/provinces.out" "$region"
  done
  for region in hk 香港 mo 澳门 tw 台湾 hb,hk; do
    if report_env bash "$RUNNER" -p "$region" >"$TEST_DIR/unavailable-region.out" 2>&1; then
      fail '尚未开放的公共测速地区被接受'
    fi
    assert_contains "$TEST_DIR/unavailable-region.out" '暂未开放公共测速'
    assert_not_contains "$TEST_DIR/unavailable-region.out" '开始运行'
  done

  printf '%s\n' '{"country_code":"HK","region_code":"HK","region":"Hong Kong"}' \
    > "$TEST_DIR/geo-hk.json"
  report_env SPEEDQUALITY_SSH_CLIENT_IP=8.8.8.8 \
    SPEEDQUALITY_GEO_RESPONSE_FILE="$TEST_DIR/geo-hk.json" \
    bash "$RUNNER" -p bsg >"$TEST_DIR/bsg-hk.out" 2>&1
  assert_contains "$TEST_DIR/bsg-hk.out" '测速省份: 北京,上海,广东；'

  sed 's/"hb"/"hk"/' "$FIXTURES/node-route-hb-v4.json" > "$TEST_DIR/node-route-hk.json"
  args="$TEST_DIR/hk-node-args.txt"
  report_env SPEEDQUALITY_NODE_ROUTE_FILE="$TEST_DIR/node-route-hk.json" MOCK_ARGS_FILE="$args" \
    bash "$RUNNER" --node sqn_rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr \
    >"$TEST_DIR/hk-node.out" 2>&1
  assert_line "$args" 'hk v4'
  pass '地区输入与列表一致，未开放地区提前提示且仍允许精确使用自有节点'
}

test_foreign_ssh_requires_manual_region() {
  if report_env \
    SPEEDQUALITY_SSH_CLIENT_IP=8.8.8.8 SPEEDQUALITY_GEO_RESPONSE_FILE="$FOREIGN_GEO" \
    bash "$RUNNER" >"$TEST_DIR/foreign.out" 2>&1; then
    fail '境外 SSH 来源在非交互模式下被自动接受'
  fi
  assert_contains "$TEST_DIR/foreign.out" '不在支持的中国省级地区内'
  assert_contains "$TEST_DIR/foreign.out" '-p/--province'
  pass '境外 SSH 来源要求手动指定省份'
}

test_foreign_ssh_interactive_prompt_offers_bsg_fallback() {
  local output="$TEST_DIR/foreign-interactive.out"
  local command
  command -v script >/dev/null 2>&1 || fail '缺少伪终端测试命令 script'
  printf -v command \
    'env TERM=dumb SPEEDQUALITY_REPORT_BASE=%q SPEEDQUALITY_REPORT_RESPONSE_FILE=%q SPEEDQUALITY_PROBE_BIN=%q SPEEDQUALITY_SESSION_TOKEN=%q SPEEDQUALITY_LEASE_DIR=%q SPEEDQUALITY_HAS_IPV4=1 SPEEDQUALITY_HAS_IPV6=0 SPEEDQUALITY_SSH_CLIENT_IP=8.8.8.8 SPEEDQUALITY_GEO_RESPONSE_FILE=%q bash %q' \
    "$REPORT_BASE" "$REPORT_RESPONSE" "$MOCK_PROBE" \
    'abcdefghijklmnopqrstuvwxyzABCDEFGH12345678' "$LEASE_DIR" "$FOREIGN_GEO" "$RUNNER"

  if ! printf 'hb\n\n\n' | script -qefc "$command" /dev/null >"$output" 2>&1; then
    fail '境外 SSH 来源交互选择测试失败'
  fi
  assert_contains "$output" '测速地区 [无默认值，可填 bsg、hb 或 hb,bj，最多 5 个]'
  pass '境外 SSH 来源的交互提示允许选择退化为北上广三省的 bsg'
}

test_verified_nodequality() {
  local output="$TEST_DIR/nq-match.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$MATCH_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '服务器身份校验通过'
  assert_contains "$output" '时间校验通过'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  assert_not_contains "$output" '展示类型:'
  pass '--nq 校验同一服务器和 60 分钟内的报告'
}

test_disabled_nodequality_binding_falls_back_before_fetch() {
  local output="$TEST_DIR/nq-disabled.out"
  report_env \
    SPEEDQUALITY_NQ_BINDING_ENABLED=0 \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$TEST_DIR/does-not-exist-ipinfo.json" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$TEST_DIR/does-not-exist-record.json" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '当前已暂停 NodeQuality 关联'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  assert_not_contains "$output" '正在校验 NodeQuality'
  assert_not_contains "$output" '展示类型:'
  pass '运营开关关闭时不读取 NQ 并回退到独立报告'
}

test_nodequality_does_not_require_python() {
  local output="$TEST_DIR/nq-without-python.out"
  local marker="$TEST_DIR/python-was-called"
  local mock_bin="$TEST_DIR/no-python-bin"
  mkdir -p "$mock_bin"
  cat > "$mock_bin/python3" <<EOF
#!/usr/bin/env bash
touch "$marker"
exit 127
EOF
  chmod +x "$mock_bin/python3"

  PATH="$mock_bin:$PATH" report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$MATCH_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  [[ ! -e "$marker" ]] || fail 'NodeQuality 校验调用了 Python'
  assert_contains "$output" '服务器身份校验通过'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass 'NodeQuality 校验不依赖 Python 或虚拟环境'
}

test_latest_nodequality_time_wins() {
  local output="$TEST_DIR/nq-latest.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$LATEST_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_TOKEN" >"$output" 2>&1

  assert_contains "$output" '时间校验通过'
  assert_not_contains "$output" '检测时间差过大'
  pass 'NodeQuality 多个时间中选择最晚时间'
}

test_gb18030_markdown_time_is_detected() {
  local output="$TEST_DIR/nq-markdown-time.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$MARKDOWN_TIME_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '时间校验通过'
  assert_not_contains "$output" '检测时间差过大'
  assert_not_contains "$output" '无法确认 NodeQuality'
  pass 'GB18030 Markdown 中的 NodeQuality 检测时间会参与校验'
}

test_stale_nodequality_is_highlighted() {
  local output="$TEST_DIR/nq-stale.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$STALE_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '检测时间差过大'
  assert_contains "$output" '阈值 60 分钟'
  assert_contains "$output" '仍会绑定'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass '超过固定 60 分钟时仍绑定并高亮'
}

test_unknown_time_is_highlighted() {
  local output="$TEST_DIR/nq-time-unknown.out"
  report_env SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$TIMELESS_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '无法确认 NodeQuality 与本次测速的时间差'
  assert_contains "$output" '仍会绑定并高亮'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass '无法解析检测时间时仍绑定并提示'
}

test_mismatched_nodequality_falls_back() {
  local output="$TEST_DIR/nq-mismatch.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$MISMATCH_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" 'NodeQuality 报告与当前服务器身份不匹配'
  assert_contains "$output" '分享链接只包含本次测速结果'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  assert_not_contains "$output" '展示类型:'
  pass '服务器不一致时拒绝绑定并生成独立报告'
}

test_broad_mask_is_rejected() {
  local output="$TEST_DIR/nq-broad-mask.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$BROAD_MASK_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1
  assert_contains "$output" 'masked_ip_too_broad'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass 'NodeQuality 掩码 IP 不足两段时拒绝绑定'
}

test_nodequality_snapshot_is_sanitized_and_allowlisted() {
  local output="$TEST_DIR/nq-snapshot.out"
  local snapshot="$TEST_DIR/nq-snapshot.json"
  local mock_bin="$TEST_DIR/mock-curl-bin"
  mkdir -p "$mock_bin"
  ln -s "$MOCK_CURL" "$mock_bin/curl"

  env PATH="$mock_bin:$PATH" \
    MOCK_CURL_REPORT_URL="$REPORT_PAGE" \
    MOCK_CURL_SNAPSHOT_CAPTURE="$snapshot" \
    SPEEDQUALITY_REPORT_BASE="$REPORT_BASE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_NQ_VERIFY_BIN="$REAL_PROBE" \
    SPEEDQUALITY_SESSION_TOKEN="abcdefghijklmnopqrstuvwxyzABCDEFGH12345678" \
    SPEEDQUALITY_LEASE_DIR="$LEASE_DIR" SPEEDQUALITY_HAS_IPV4=1 \
    SPEEDQUALITY_HAS_IPV6=0 \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$MATCH_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" '已生成安全的 NodeQuality 展示快照'
  [[ -s "$snapshot" ]] || fail '没有捕获到 NodeQuality 快照'
  if ! python3 - "$snapshot" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    snapshot = json.load(handle)
assert snapshot["version"] == 2
pages = snapshot["pages"]
assert [page["id"] for page in pages] == [
    "basic", "ip-quality", "network-quality", "return-route",
]
assert [page["title"] for page in pages] == [
    "基本信息", "IP质量", "网络质量", "回程路由",
]
assert [page["format"] for page in pages] == ["ansi", "ansi", "image", "image"]
assert all(page["source"] == "nodequality.md" for page in pages)
content = "\n".join(page["content"] for page in pages)
assert "  保留  对齐空格" in content
assert "\x1b[31m红色标题\x1b[0m" in content
assert "\x1b]" not in content
assert "\x01" not in content
assert "<script>alert('snapshot')</script>" in content
assert pages[2]["image_url"] == "https://images.example/network.webp"
assert pages[3]["image_url"] == "https://images.example/route.png"
PY
  then
    fail 'NodeQuality 原始分页快照或安全清洗不符合预期'
  fi
  pass 'NodeQuality 分页顺序、ANSI、空白和图片被保留且危险控制符已清除'
}

test_nodequality_archive_member_limit() {
  local output="$TEST_DIR/nq-too-many.out"
  report_env \
    SPEEDQUALITY_NODEQUALITY_IPINFO_FILE="$CURRENT_INFO" \
    SPEEDQUALITY_NODEQUALITY_RECORD_FILE="$TOO_MANY_RECORD" \
    bash "$RUNNER" -p hb --nq "$REPORT_URL" >"$output" 2>&1

  assert_contains "$output" 'NodeQuality 报告解析失败'
  assert_contains "$output" "分享报告: $REPORT_PAGE"
  pass 'NodeQuality ZIP 成员数量超过上限时拒绝绑定'
}

test_report_failure_falls_back_to_image() {
  local bad_response="$TEST_DIR/bad-report-response.txt"
  printf '%s\n' 'https://evil.example/r/AbCdEfGhIjKl' > "$bad_response"
  report_env SPEEDQUALITY_REPORT_RESPONSE_FILE="$bad_response" \
    bash "$RUNNER" -p hb >"$TEST_DIR/report-fallback.out" 2>&1
  assert_contains "$TEST_DIR/report-fallback.out" '分享服务返回了无效链接'
  assert_not_contains "$TEST_DIR/report-fallback.out" '分享报告:'
  assert_not_contains "$TEST_DIR/report-fallback.out" '展示链接:'
  pass '分享服务失败时保留终端结果'
}

test_worker_injected_report_base() {
  local injected="$TEST_DIR/run-injected.sh"
  sed "s|__SPEEDQUALITY_REPORT_BASE__|$REPORT_BASE|g" "$RUNNER" > "$injected"
  env SPEEDQUALITY_REPORT_RESPONSE_FILE="$REPORT_RESPONSE" \
    SPEEDQUALITY_PROBE_BIN="$MOCK_PROBE" \
    SPEEDQUALITY_SESSION_TOKEN="abcdefghijklmnopqrstuvwxyzABCDEFGH12345678" \
    SPEEDQUALITY_LEASE_DIR="$LEASE_DIR" SPEEDQUALITY_HAS_IPV4=1 \
    SPEEDQUALITY_HAS_IPV6=0 \
    bash "$injected" -p hb >"$TEST_DIR/injected-base.out" 2>&1
  assert_contains "$TEST_DIR/injected-base.out" "分享报告: $REPORT_PAGE"
  assert_not_contains "$TEST_DIR/injected-base.out" '当前入口未配置分享服务'
  bash "$injected" --help >"$TEST_DIR/injected-help.out" 2>&1
  assert_contains "$TEST_DIR/injected-help.out" \
    "bash <(curl -fsSL $REPORT_BASE/run) -p hb -s 200"
  pass 'Worker 注入的域名会启用分享服务'
}

test_worker_injected_node_installer_help() {
  local injected="$TEST_DIR/install-node-injected.sh"
  sed -e "s|__SPEEDQUALITY_REPORT_BASE__|$REPORT_BASE|g" \
    -e 's|__SPEEDQUALITY_PROBE_VERSION__|v1.0.11|g' \
    "$ROOT_DIR/install-node.sh" > "$injected"
  bash "$injected" --help >"$TEST_DIR/install-node-help.out" 2>&1
  assert_contains "$TEST_DIR/install-node-help.out" \
    "bash <(curl -fsSL $REPORT_BASE/install-node)"
  pass '节点安装器帮助显示 Worker 注入的真实一键命令'
}

test_version_and_safe_cleanup() {
  local temp_parent="$TEST_DIR/user-temp"
  mkdir -p "$temp_parent/user-library"
  printf 'keep\n' > "$temp_parent/user-library/package.dat"

  bash "$RUNNER" --version >"$TEST_DIR/version.out" 2>&1
  assert_contains "$TEST_DIR/version.out" 'SpeedQuality 1.0.11'
  assert_contains "$TEST_DIR/version.out" 'Probe v1.0.11'

  TMPDIR="$temp_parent" report_env \
    bash "$RUNNER" -p hb >"$TEST_DIR/cleanup.out" 2>&1
  [[ -f "$temp_parent/user-library/package.dat" ]] || fail '用户文件被清理逻辑删除'
  if find "$temp_parent" -mindepth 1 -maxdepth 1 -type d -name 'speedquality.*' | grep -q .; then
    fail '本次测速临时目录未被清理'
  fi
  pass '--version 不测速且清理逻辑只删除本次临时目录'
}

test_removed_options_and_no_markdown() {
  local work="$TEST_DIR/work"
  mkdir -p "$work"
  if report_env \
    bash "$RUNNER" -p hb --max-time-gap 30 >"$TEST_DIR/removed.out" 2>&1; then
    fail '已移除的 --max-time-gap 被接受'
  fi
  assert_contains "$TEST_DIR/removed.out" '参数 --max-time-gap 已移除'
  if report_env \
    bash "$RUNNER" -p hb --speed-result https://example.com/result.png \
      >"$TEST_DIR/removed-speed-result.out" 2>&1; then
    fail '已移除的 --speed-result 被接受'
  fi
  assert_contains "$TEST_DIR/removed-speed-result.out" '参数 --speed-result 已移除'

  (cd "$work" && report_env \
    bash "$RUNNER" -p hb >"$TEST_DIR/no-markdown.out" 2>&1)
  if find "$work" -maxdepth 1 -type f -name '*.md' | grep -q .; then
    fail '运行后生成了本地 Markdown'
  fi
  pass '旧参数被拒绝且运行后不生成本地 Markdown'
}

(cd "$ROOT_DIR/probe" && GOCACHE="${GOCACHE:-/tmp/sq-probe-test-gocache}" go build -o "$REAL_PROBE" .)
prepare_fixtures
bash -n "$RUNNER" "$MOCK_PROBE" "$0"
test_short_province_and_default_speed
test_auto_ssh_region
test_speed_aliases_and_validation
test_ip_family_selection
test_default_dual_uses_family_bound_sessions
test_ipv6_only_uses_v6_control_plane
test_node_directory_error_is_explained_without_retry
test_platform_clock_skew
test_bsg_preset_and_province_limit
test_exact_community_node_route
test_traffic_estimate_and_measurement
test_chinese_provinces_and_city_rejection
test_foreign_ssh_requires_manual_region
test_foreign_ssh_interactive_prompt_offers_bsg_fallback
test_verified_nodequality
test_disabled_nodequality_binding_falls_back_before_fetch
test_nodequality_does_not_require_python
test_latest_nodequality_time_wins
test_gb18030_markdown_time_is_detected
test_stale_nodequality_is_highlighted
test_unknown_time_is_highlighted
test_mismatched_nodequality_falls_back
test_broad_mask_is_rejected
test_nodequality_snapshot_is_sanitized_and_allowlisted
test_nodequality_archive_member_limit
test_report_failure_falls_back_to_image
test_worker_injected_report_base
test_worker_injected_node_installer_help
test_version_and_safe_cleanup
test_removed_options_and_no_markdown

printf '1..%d\n' "$TESTS"
