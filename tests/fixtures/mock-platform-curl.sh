#!/usr/bin/env bash

set -euo pipefail

output_file=""
url=""
curl_family=""
lease_family=""
authorization=""
write_out=""

while (($#)); do
  case "$1" in
    -4|-6)
      curl_family="$1"
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
    cp -- "${MOCK_PLATFORM_LEASE_DIR:?}/default-${lease_family}.json" "$output_file"
    ;;
  */api/results)
    printf 'report %s %s\n' "$curl_family" "$authorization" >> "$MOCK_PLATFORM_LOG"
    printf '%s\n' "${MOCK_PLATFORM_REPORT_URL:-https://reports.example/r/AbCdEfGhIjKl}" \
      > "$output_file"
    ;;
  *)
    exit 1
    ;;
esac

if [[ -n "$write_out" ]]; then
  printf '200'
fi
