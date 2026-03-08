#!/usr/bin/env bash
# check-api-schema.sh
#
# Hits Confluence Cloud API endpoints and extracts top-level JSON field names.
# Writes the result to docs/api-snapshot.json.
#
# Usage (local):
#   CONFLUENCE_TOKEN=<PAT> CONFLUENCE_DOMAIN=myorg.atlassian.net ./scripts/check-api-schema.sh
#
# Exit codes:
#   0 — no drift (or snapshot just created)
#   1 — drift detected (CI should open an issue)

set -euo pipefail

TOKEN="${CONFLUENCE_TOKEN:?CONFLUENCE_TOKEN must be set}"
DOMAIN="${CONFLUENCE_DOMAIN:?CONFLUENCE_DOMAIN must be set}"
BASE="https://${DOMAIN}"
SNAPSHOT="docs/api-snapshot.json"
TMPFILE="$(mktemp)"
trap 'rm -f "$TMPFILE"' EXIT

fetch_keys() {
  local path="$1"
  local response
  response=$(curl -sf \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Accept: application/json" \
    --max-time 15 \
    "${BASE}${path}") || { echo "WARN: ${BASE}${path} returned error — skipping" >&2; echo "[]"; return; }

  # Confluence v2 list endpoints return { "results": [...], "_links": {...} }
  # Extract keys from the first result item, or top-level keys for non-list responses.
  echo "$response" | jq -r '
    if .results | type == "array" and length > 0 then
      .results[0] | keys
    else
      keys
    end
  ' 2>/dev/null || echo "[]"
}

declare -A PATHS=(
  [spaces]="/wiki/api/v2/spaces?limit=1"
  [pages]="/wiki/api/v2/pages?limit=1"
  [blogposts]="/wiki/api/v2/blogposts?limit=1"
)

echo "Fetching API schema from Confluence Cloud (${DOMAIN})..." >&2

ENDPOINTS_JSON="{}"
for name in "${!PATHS[@]}"; do
  keys=$(fetch_keys "${PATHS[$name]}")
  ENDPOINTS_JSON=$(echo "$ENDPOINTS_JSON" | jq --arg n "$name" --argjson k "$keys" '.[$n] = $k')
  echo "  $name: $(echo "$keys" | jq -r 'length') fields" >&2
done

NEW_SNAPSHOT=$(jq -n \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson ep "$ENDPOINTS_JSON" \
  '{"generated": $ts, "endpoints": $ep | to_entries | sort_by(.key) | from_entries}')

echo "$NEW_SNAPSHOT" > "$TMPFILE"

if [[ ! -f "$SNAPSHOT" ]]; then
  cp "$TMPFILE" "$SNAPSHOT"
  echo "Snapshot created at $SNAPSHOT — no baseline existed yet." >&2
  exit 0
fi

OLD_EP=$(jq '.endpoints' "$SNAPSHOT")
NEW_EP=$(jq '.endpoints' "$TMPFILE")

if [[ "$OLD_EP" == "$NEW_EP" ]]; then
  echo "No API drift detected." >&2
  exit 0
fi

echo "API DRIFT DETECTED:" >&2
diff <(echo "$OLD_EP" | jq -S .) <(echo "$NEW_EP" | jq -S .) >&2 || true
diff <(echo "$OLD_EP" | jq -S .) <(echo "$NEW_EP" | jq -S .) > drift.diff || true

exit 1
