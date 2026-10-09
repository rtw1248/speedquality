#!/usr/bin/env bash

set -euo pipefail

if [[ "${1:-}" == "nq-verify" ]]; then
  exec "${SPEEDQUALITY_NQ_VERIFY_BIN:?}" "$@"
fi

lease=""
output=""
reference_time=""
while (($# > 0)); do
  case "$1" in
    --lease) lease="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
    --reference-time) reference_time="$2"; shift 2 ;;
    --append) shift ;;
    *) shift ;;
  esac
done

[[ -n "$lease" && -n "$output" ]]
name=$(basename -- "$lease")
region=$(sed -n 's/^lease-\([a-z][a-z]*\)-\(v[46]\)-[12]\.json$/\1/p' <<< "$name")
family=$(sed -n 's/^lease-\([a-z][a-z]*\)-\(v[46]\)-[12]\.json$/\2/p' <<< "$name")
[[ -n "$region" && -n "$family" ]]

case "$region" in
  hb) region_name="湖北" ;;
  bj) region_name="北京" ;;
  *) region_name="$region" ;;
esac

if [[ -n "${MOCK_ARGS_FILE:-}" ]]; then
  printf '%s %s\n' "$region" "$family" >> "$MOCK_ARGS_FILE"
fi

if [[ -n "${MOCK_REFERENCE_TIMES_FILE:-}" ]]; then
  printf '%s\n' "$reference_time" >> "$MOCK_REFERENCE_TIMES_FILE"
fi

if [[ -n "${MOCK_TIP_STATES_FILE:-}" ]]; then
  printf '%s\n' "${SPEEDQUALITY_TIP_STATE_FILE:-}" >> "$MOCK_TIP_STATES_FILE"
fi

now="${reference_time:-$(date +%s)}"
target_mbps="${MOCK_TARGET_MBPS:-200}"
status="ok"
error=""
upload_mbps="150.50"
upload_bytes="94062500"
upload_display="150.50Mbps"
if [[ "$family" == "${MOCK_FAILED_FAMILY:-}" ]]; then
  status="failed"
  error="上传未产生有效数据"
  upload_mbps="0"
  upload_bytes="0"
  upload_display="失败"
fi
printf '{"version":1,"lease_id":"lease_mock_%s_%s","started_at":%s,"completed_at":%s,"region":{"code":"%s","name":"%s"},"family":"%s","duration_seconds":5,"target_mbps":%s,"modes":["s"],"results":[{"carrier":"ct","label":"%s电信","node_id":"0123456789abcdef0123456789abcdef","latency_ms":8.25,"status":"%s","error":"%s","single":{"download_mbps":199.20,"upload_mbps":%s,"download_bytes":124500000,"upload_bytes":%s}}]}\n' \
  "$region" "$family" "$now" "$now" "$region" "$region_name" "$family" "$target_mbps" "$region_name" \
  "$status" "$error" "$upload_mbps" "$upload_bytes" > "$output"

printf '\nIPv%s            延迟        单线程上传        单线程下载\n' "${family#v}"
printf '电信          8ms       %s       %sMbps ✓\n' "$upload_display" "$target_mbps"

if [[ -n "${MOCK_NETDEV_FILE:-}" ]]; then
  cat > "$MOCK_NETDEV_FILE" <<EOF
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: ${MOCK_NETDEV_AFTER_RX:-100001000} 0 0 0 0 0 0 0 ${MOCK_NETDEV_AFTER_TX:-50002000} 0 0 0 0 0 0 0
EOF
fi

[[ "$status" == "ok" ]]
