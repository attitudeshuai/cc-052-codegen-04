#!/usr/bin/env bash
# End-to-end verification of device registry + sync reconciliation.
# Prereq: service running (docker compose up -d --build), jq installed.
# Usage:  ./scripts/verify_device_sync.sh [BASE_URL]
set -euo pipefail

BASE="${1:-http://localhost:9082}"
PASS=0; FAIL=0

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '  \033[1;32m✓\033[0m %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[1;31m✗ %s\033[0m\n' "$*"; }
check() { # check <desc> <actual> <expected>
  if [ "$2" = "$3" ]; then ok "$1 ($2)"; else bad "$1: got '$2', want '$3'"; fi
}

post() { curl -sS -X POST "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
get()  { curl -sS "$BASE$1"; }
put()  { curl -sS -X PUT "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }

say "health"
check "healthz" "$(get /healthz | jq -r '.status // .data.status // "ok"')" "ok"

say "prepare farm / plot / batch"
FARM_ID=$(post /api/v1/farms '{"name":"合作社A","region_code":"510114"}' | jq -r '.data.id')
PLOT_ID=$(post /api/v1/plots "{\"farm_id\":$FARM_ID,\"name\":\"3号地\",\"area_mu\":12.5}" | jq -r '.data.id')
BATCH_ID=$(post /api/v1/batches "{\"plot_id\":$PLOT_ID,\"crop_id\":\"rice-001\",\"sowing_date\":\"2026-04-01\",\"expected_yield_kg\":8000}" | jq -r '.data.id')
ok "farm=$FARM_ID plot=$PLOT_ID batch=$BATCH_ID"

say "1. register device"
R=$(post /api/v1/devices "{\"device_code\":\"DEV-001\",\"plot_id\":$PLOT_ID,\"operator\":\"老张\"}")
DEV_ID=$(echo "$R" | jq -r '.data.device.id')
check "registered" "$(echo "$R" | jq -r '.data.already_registered')" "false"
check "operator" "$(echo "$R" | jq -r '.data.device.operator')" "老张"

say "2. sync seq 1..5 (first pass)"
recs=""
for i in 1 2 3 4 5; do
  recs+="{\"seq\":$i,\"client_uuid\":\"u-$i\",\"batch_id\":$BATCH_ID,\"kind\":\"irrigation\",\"happened_at\":\"2026-04-1$i\",\"operator\":\"老张\"},"
done
R=$(post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[${recs%,}]}")
check "inserted" "$(echo "$R" | jq -r '.data.inserted')" "5"
check "next_seq" "$(echo "$R" | jq -r '.data.checkpoint.next_seq')" "6"

say "3. re-sync same 1..5 (network retry) -> all duplicate, no double store"
R=$(post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[${recs%,}]}")
check "duplicated" "$(echo "$R" | jq -r '.data.duplicated')" "5"
CNT=$(get "/api/v1/batches/$BATCH_ID/activities" | jq '.data | length')
check "activities still 5" "$CNT" "5"

say "4. broken sync: only 6,9,10 land (7,8 lost)"
recs=""
for i in 6 9 10; do
  day=$((10+i))
  recs+="{\"seq\":$i,\"client_uuid\":\"u-$i\",\"batch_id\":$BATCH_ID,\"kind\":\"weed\",\"happened_at\":\"2026-04-$day\",\"operator\":\"老张\"},"
done
post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[${recs%,}]}" > /dev/null
R=$(get "/api/v1/devices/$DEV_ID/sync-status")
check "max_contiguous_seq" "$(echo "$R" | jq -r '.data.max_contiguous_seq')" "6"
check "next_seq resumes at first gap" "$(echo "$R" | jq -r '.data.next_seq')" "7"
check "gaps" "$(echo "$R" | jq -c '.data.gaps')" "[7,8]"

say "5. reconcile: device claims 1..10 -> gaps pointed out one by one"
R=$(get "/api/v1/devices/$DEV_ID/reconcile?max_seq=10")
check "received" "$(echo "$R" | jq -r '.data.received')" "8"
check "missing_count" "$(echo "$R" | jq -r '.data.missing_count')" "2"
check "missing_seqs" "$(echo "$R" | jq -c '.data.missing_seqs')" "[7,8]"

say "6. fill the gaps 7,8 -> contiguous again"
recs=""
for i in 7 8; do
  day=$((10+i))
  recs+="{\"seq\":$i,\"client_uuid\":\"u-$i\",\"batch_id\":$BATCH_ID,\"kind\":\"weed\",\"happened_at\":\"2026-04-$day\",\"operator\":\"老张\"},"
done
post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[${recs%,}]}" > /dev/null
R=$(get "/api/v1/devices/$DEV_ID/sync-status")
check "max_contiguous_seq" "$(echo "$R" | jq -r '.data.max_contiguous_seq')" "10"
check "gaps empty" "$(echo "$R" | jq -c '.data.gaps')" "[]"
R=$(get "/api/v1/devices/$DEV_ID/reconcile?max_seq=10")
check "missing_count 0" "$(echo "$R" | jq -r '.data.missing_count')" "0"

say "7. rejected record is not reported as a gap"
R=$(post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[{\"seq\":11,\"client_uuid\":\"u-11\",\"batch_id\":$BATCH_ID,\"kind\":\"fertilize\",\"happened_at\":\"2026-03-01\",\"operator\":\"老张\"}]}")
check "rejected (before sowing)" "$(echo "$R" | jq -r '.data.rejected')" "1"
R=$(get "/api/v1/devices/$DEV_ID/reconcile?max_seq=11")
check "missing stays 0" "$(echo "$R" | jq -r '.data.missing_count')" "0"
check "rejected listed" "$(echo "$R" | jq -r '.data.rejected[0].seq')" "11"

say "8. reinstall: re-register same device_code -> same device, progress kept"
R=$(post /api/v1/devices "{\"device_code\":\"DEV-001\",\"plot_id\":$PLOT_ID,\"operator\":\"老张\"}")
check "already_registered" "$(echo "$R" | jq -r '.data.already_registered')" "true"
check "same device id" "$(echo "$R" | jq -r '.data.device.id')" "$DEV_ID"
R=$(get "/api/v1/devices/$DEV_ID/sync-status")
check "next_seq still 12" "$(echo "$R" | jq -r '.data.next_seq')" "12"

say "9. operator handover does not touch progress"
R=$(put "/api/v1/devices/$DEV_ID" '{"operator":"小李"}')
check "operator updated" "$(echo "$R" | jq -r '.data.operator')" "小李"
R=$(get "/api/v1/devices/$DEV_ID/sync-status")
check "next_seq unchanged" "$(echo "$R" | jq -r '.data.next_seq')" "12"

say "10. seq conflict: reuse seq 11 with a new uuid -> conflict, not stored"
R=$(post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[{\"seq\":11,\"client_uuid\":\"u-11-NEW\",\"batch_id\":$BATCH_ID,\"kind\":\"fertilize\",\"happened_at\":\"2026-04-15\"}]}")
check "conflicted" "$(echo "$R" | jq -r '.data.conflicted')" "1"

say "11. new seq 12 with default operator (uses device registration)"
R=$(post "/api/v1/devices/$DEV_ID/sync" "{\"records\":[{\"seq\":12,\"client_uuid\":\"u-12\",\"batch_id\":$BATCH_ID,\"kind\":\"irrigation\",\"happened_at\":\"2026-04-25\"}]}")
check "inserted" "$(echo "$R" | jq -r '.data.inserted')" "1"
OP=$(get "/api/v1/batches/$BATCH_ID/activities" | jq -r '.data[] | select(.client_uuid=="u-12") | .operator')
check "operator defaulted to 小李" "$OP" "小李"

say "12. total activities = 11 (10 accepted + 1 default-operator), replays added nothing"
CNT=$(get "/api/v1/batches/$BATCH_ID/activities" | jq '[.data[] | select(.id != null)] | length')
check "activity count" "$CNT" "11"

echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[1;32mALL %d CHECKS PASSED\033[0m\n' "$PASS"
else
  printf '\033[1;31m%d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
