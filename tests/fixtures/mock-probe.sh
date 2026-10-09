#!/usr/bin/env bash

set -euo pipefail

if [[ "${1:-}" == "nq-verify" ]]; then
  exec "${SPEEDQUALITY_NQ_VERIFY_BIN:?}" "$@"
fi

lease=""
output=""
while (($# > 0)); do
  case "$1" in
    --lease) lease="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
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

now=$(date +%s)
target_mbps="${MOCK_TARGET_MBPS:-200}"
printf '{"version":1,"lease_id":"lease_mock_%s_%s","started_at":%s,"completed_at":%s,"region":{"code":"%s","name":"%s"},"family":"%s","duration_seconds":5,"target_mbps":%s,"modes":["s"],"results":[{"carrier":"ct","label":"%s电信","node_id":"0123456789abcdef0123456789abcdef","latency_ms":8.25,"status":"ok","single":{"download_mbps":199.20,"upload_mbps":150.50,"download_bytes":124500000,"upload_bytes":94062500}}]}\n' \
  "$region" "$family" "$now" "$now" "$region" "$region_name" "$family" "$target_mbps" "$region_name" > "$output"

printf '\nIPv%s            延迟        单线程上传        单线程下载\n' "${family#v}"
printf '%s电信       8.25ms       150.50Mbps       %sMbps ✓\n' "$region_name" "$target_mbps"

if [[ -n "${MOCK_NETDEV_FILE:-}" ]]; then
  cat > "$MOCK_NETDEV_FILE" <<EOF
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: ${MOCK_NETDEV_AFTER_RX:-100001000} 0 0 0 0 0 0 0 ${MOCK_NETDEV_AFTER_TX:-50002000} 0 0 0 0 0 0 0
EOF
fi
