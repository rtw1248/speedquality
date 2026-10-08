#!/usr/bin/env bash

set -euo pipefail

output_file=""
snapshot_file=""
report_url="${MOCK_CURL_REPORT_URL:-}"
snapshot_capture="${MOCK_CURL_SNAPSHOT_CAPTURE:-}"

while (($#)); do
  case "$1" in
    -o)
      output_file="$2"
      shift 2
      ;;
    --form)
      if [[ "$2" == nq_snapshot=@* ]]; then
        snapshot_file="${2#nq_snapshot=@}"
        snapshot_file="${snapshot_file%%;type=*}"
      fi
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

[[ -n "$output_file" && -n "$report_url" ]]
if [[ -n "$snapshot_capture" && -n "$snapshot_file" ]]; then
  cp -- "$snapshot_file" "$snapshot_capture"
fi
printf '%s\n' "$report_url" > "$output_file"
