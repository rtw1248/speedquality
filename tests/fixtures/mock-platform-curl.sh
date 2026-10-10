#!/usr/bin/env bash

set -euo pipefail

output_file=""
url=""
curl_family=""
lease_family=""
authorization=""
write_out=""
response_status="200"
tested_at=""
noproxy=""
fail_on_http=0

while (($#)); do
  case "$1" in
    -4|-6)
      curl_family="$1"
      shift
      ;;
    --noproxy)
      noproxy="$2"
      shift 2
      ;;
    -f*|--fail)
      fail_on_http=1
      shift
      ;;
    -o)
      output_file="$2"
      shift 2
      ;;
    -H)
      if [[ "$2" == Authorization:* ]]; then
        authorization="${2#Authorization: }"
      fi
      shift 2
      ;;
    --data-urlencode)
      if [[ "$2" == family=* ]]; then
        lease_family="${2#family=}"
      elif [[ "$2" == tested_at=* ]]; then
        tested_at="${2#tested_at=}"
      fi
      shift 2
      ;;
    --write-out|-w)
      write_out="$2"
      shift 2
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

[[ -n "$output_file" && -n "$url" && -n "${MOCK_PLATFORM_LOG:-}" ]]
case "$url" in
  */api/time)
    if [[ "$output_file" == /dev/null ]]; then
      [[ "$noproxy" == '*' ]] || exit 99
      printf 'connectivity %s\n' "$curl_family" >> "$MOCK_PLATFORM_LOG"
      attempt=$(grep -Fc -- "connectivity $curl_family" "$MOCK_PLATFORM_LOG")
      if [[ "${MOCK_PREFLIGHT_EXIT:-0}" != 0 ]] && \
        ((attempt <= ${MOCK_PREFLIGHT_FAILURES:-2})); then
        printf '000'
        exit "$MOCK_PREFLIGHT_EXIT"
      fi
      response_status="${MOCK_PREFLIGHT_HTTP_STATUS:-200}"
      if ((fail_on_http == 1 && response_status >= 400)); then exit 22; fi
    fi
    printf 'time %s\n' "$curl_family" >> "$MOCK_PLATFORM_LOG"
    printf '{"version":1,"epoch":%s}\n' "${MOCK_PLATFORM_EPOCH:-$(date +%s)}" > "$output_file"
    ;;
  */checksums.txt)
    cp -- "${MOCK_PLATFORM_CHECKSUMS_FILE:?}" "$output_file"
    ;;
  */sqprobe-linux-amd64|*/sqprobe-linux-arm64)
    cp -- "${MOCK_PLATFORM_PROBE_SOURCE:?}" "$output_file"
    if [[ -n "${MOCK_DOWNLOAD_NETDEV_FILE:-}" ]]; then
      cat > "$MOCK_DOWNLOAD_NETDEV_FILE" <<EOF
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: ${MOCK_DOWNLOAD_AFTER_RX:-6001000} 0 0 0 0 0 0 0 ${MOCK_DOWNLOAD_AFTER_TX:-2000} 0 0 0 0 0 0 0
EOF
    fi
    ;;
  */api/session)
    printf 'session %s\n' "$curl_family" >> "$MOCK_PLATFORM_LOG"
    if [[ "$curl_family" == "-6" ]]; then
      printf '%s\n' 'ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210v6token' > "$output_file"
    else
      printf '%s\n' 'abcdefghijklmnopqrstuvwxyzABCDEFGH12345678' > "$output_file"
    fi
    ;;
  */api/node-lease)
    printf 'lease %s %s %s\n' "$lease_family" "$curl_family" "$authorization" \
      >> "$MOCK_PLATFORM_LOG"
    if [[ -n "${MOCK_PLATFORM_LEASE_ERROR:-}" ]]; then
      printf '{"error":"%s"}\n' "$MOCK_PLATFORM_LEASE_ERROR" > "$output_file"
      response_status="${MOCK_PLATFORM_LEASE_HTTP_STATUS:-502}"
    else
      cp -- "${MOCK_PLATFORM_LEASE_DIR:?}/default-${lease_family}.json" "$output_file"
    fi
    ;;
  */api/results)
    if [[ -n "${MOCK_PLATFORM_REPORT_TIME_FILE:-}" ]]; then
      printf '%s\n' "$tested_at" > "$MOCK_PLATFORM_REPORT_TIME_FILE"
    fi
    printf 'report %s %s\n' "$curl_family" "$authorization" >> "$MOCK_PLATFORM_LOG"
    printf '%s\n' "${MOCK_PLATFORM_REPORT_URL:-https://reports.example/r/AbCdEfGhIjKl}" \
      > "$output_file"
    ;;
  *)
    exit 1
    ;;
esac

if [[ -n "$write_out" ]]; then
  printf '%s' "$response_status"
fi
