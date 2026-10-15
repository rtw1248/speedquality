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
nq_url=""
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
    --data-urlencode|--form-string)
      if [[ "$2" == family=* ]]; then
        lease_family="${2#family=}"
      elif [[ "$2" == tested_at=* ]]; then
        tested_at="${2#tested_at=}"
      elif [[ "$2" == nq_url=* ]]; then
        nq_url="${2#nq_url=}"
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
if [[ -n "${MOCK_PLATFORM_EXPECTED_BASE:-}" && "$url" != "${MOCK_PLATFORM_EXPECTED_BASE%/}/"* ]]; then
  printf 'Unexpected platform URL: %s\n' "$url" >&2
  exit 99
fi
case "$url" in
  */api/reports/preflight)
    printf 'sq-preflight %s\n' "$curl_family" >> "$MOCK_PLATFORM_LOG"
    response_status="${MOCK_SQ_PREFLIGHT_STATUS:-200}"
    if [[ -n "${MOCK_SQ_PREFLIGHT_RESPONSE:-}" ]]; then
      cp -- "$MOCK_SQ_PREFLIGHT_RESPONSE" "$output_file"
    else
      printf '{"token":"e30.%064d","nq_url":"%s"}\n' 0 "${MOCK_SQ_NQ_URL:-}" > "$output_file"
    fi
    ;;
  */api/v1/ipinfo)
    cp -- "${MOCK_NQ_IPINFO:?}" "$output_file"
    ;;
  */api/v1/record/*)
    printf 'nq-verify %s\n' "${url##*/}" >> "$MOCK_PLATFORM_LOG"
    cp -- "${MOCK_NQ_RECORD_DIR:?}/${url##*/}.json" "$output_file"
    ;;
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
    printf 'download-checksums\n' >> "$MOCK_PLATFORM_LOG"
    [[ "${MOCK_CHECKSUM_DOWNLOAD_EXIT:-0}" == 0 ]] || exit "$MOCK_CHECKSUM_DOWNLOAD_EXIT"
    cp -- "${MOCK_PLATFORM_CHECKSUMS_FILE:?}" "$output_file"
    ;;
  */sqprobe-linux-amd64|*/sqprobe-linux-arm64)
    printf 'download-probe\n' >> "$MOCK_PLATFORM_LOG"
    if [[ "${MOCK_PROBE_DOWNLOAD_EXIT:-0}" != 0 ]]; then
      printf 'partial' > "$output_file"
      printf '%s' "${MOCK_PROBE_DOWNLOAD_HTTP:-000}"
      exit "$MOCK_PROBE_DOWNLOAD_EXIT"
    fi
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
    if [[ -n "${MOCK_NQ_SELECTED_FILE:-}" ]]; then
      printf '%s\n' "$nq_url" > "$MOCK_NQ_SELECTED_FILE"
    fi
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
