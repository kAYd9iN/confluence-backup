#!/usr/bin/env bash
# check-api-schema.sh
#
# Compares Atlassian's *published* Confluence Cloud OpenAPI specs against the
# stored baseline (docs/api-snapshot.json). The API definition is identical
# for every Confluence Cloud instance, so no instance, credentials, or
# tokens are required — the check runs entirely against public documentation.
#
# Tracked: every endpoint the tool actually calls (see internal/api/confluence.go).
# Sentinel values in the snapshot:
#   __ENDPOINT_MISSING__   — endpoint no longer documented (removed/deprecated)
#   __SCHEMA_UNPARSEABLE__ — response schema shape changed beyond recognition
#
# Usage (local): ./scripts/check-api-schema.sh
#
# Exit codes:
#   0 — no drift (or baseline just created)
#   1 — drift detected (CI opens a PR)
#   2 — spec download failed (network problem) — no snapshot written

set -euo pipefail

SNAPSHOT="docs/api-snapshot.json"
UA="confluence-backup-api-check (+https://github.com/kAYd9iN/confluence-backup)"
V2_URL="https://dac-static.atlassian.com/cloud/confluence/openapi-v2.v3.json"
V1_URL="https://dac-static.atlassian.com/cloud/confluence/swagger.v3.json"

# Pick a python that actually runs (on Windows, `python3` may resolve to the
# Microsoft Store alias stub, which only prints an install hint).
PYTHON=""
for candidate in python3 python; do
  if "$candidate" -c "pass" >/dev/null 2>&1; then PYTHON="$candidate"; break; fi
done
[[ -n "$PYTHON" ]] || { echo "ERROR: no working python3 found" >&2; exit 2; }

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

echo "Fetching published Confluence Cloud OpenAPI specs..." >&2
# dac-static.atlassian.com rejects requests without a User-Agent header.
curl -sf -A "$UA" --max-time 60 "$V2_URL" -o "$WORKDIR/v2.json" \
  || { echo "ERROR: failed to download v2 spec ($V2_URL)" >&2; exit 2; }
curl -sf -A "$UA" --max-time 60 "$V1_URL" -o "$WORKDIR/v1.json" \
  || { echo "ERROR: failed to download v1 spec ($V1_URL)" >&2; exit 2; }

"$PYTHON" - "$WORKDIR/v2.json" "$WORKDIR/v1.json" > "$WORKDIR/snapshot.json" <<'PY'
import json, sys, datetime

v2 = json.load(open(sys.argv[1], encoding="utf-8"))
v1 = json.load(open(sys.argv[2], encoding="utf-8"))

def deref(spec, obj, depth=0):
    while isinstance(obj, dict) and "$ref" in obj and depth < 30:
        target = spec
        for part in obj["$ref"].lstrip("#/").split("/"):
            target = target[part]
        obj = target
        depth += 1
    return obj

def fields(spec, path):
    item = spec.get("paths", {}).get(path)
    if not item or "get" not in item:
        return ["__ENDPOINT_MISSING__"]
    try:
        schema = deref(spec, item["get"]["responses"]["200"]["content"]["application/json"]["schema"])
        props = schema.get("properties", {})
        # List endpoints wrap items in {results: [...], _links: {...}}
        if "results" in props:
            items = deref(spec, deref(spec, props["results"]).get("items", {}))
            props = items.get("properties", {})
        return sorted(props.keys()) or ["__SCHEMA_UNPARSEABLE__"]
    except (KeyError, TypeError):
        return ["__SCHEMA_UNPARSEABLE__"]

# Every endpoint internal/api/confluence.go calls, mapped to its spec path.
ENDPOINTS = {
    "spaces":                ("v2", "/spaces"),
    "space_pages":           ("v2", "/spaces/{id}/pages"),
    "space_blogposts":       ("v2", "/spaces/{id}/blogposts"),
    "space_permissions":     ("v2", "/spaces/{id}/permissions"),
    "page_attachments":      ("v2", "/pages/{id}/attachments"),
    "page_footer_comments":  ("v2", "/pages/{id}/footer-comments"),
    "page_inline_comments":  ("v2", "/pages/{id}/inline-comments"),
    "space_property_v1":     ("v1", "/wiki/rest/api/space/{spaceKey}/property"),
    "templates_v1":          ("v1", "/wiki/rest/api/template"),
    "user_v1":               ("v1", "/wiki/rest/api/user"),
}

specs = {"v2": v2, "v1": v1}
endpoints = {name: fields(specs[ver], path) for name, (ver, path) in sorted(ENDPOINTS.items())}

print(json.dumps({
    "generated": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "sources": {
        "v2": {"url": "openapi-v2.v3.json", "version": v2.get("info", {}).get("version", "?")},
        "v1": {"url": "swagger.v3.json", "version": v1.get("info", {}).get("version", "?")},
    },
    "endpoints": endpoints,
}, indent=2))
PY

if [[ ! -f "$SNAPSHOT" ]]; then
  cp "$WORKDIR/snapshot.json" "$SNAPSHOT"
  echo "Snapshot created at $SNAPSHOT — no baseline existed yet." >&2
  exit 0
fi

extract_endpoints() {
  "$PYTHON" -c "import json,sys; print(json.dumps(json.load(open(sys.argv[1], encoding='utf-8'))['endpoints'], indent=2, sort_keys=True))" "$1"
}
OLD_EP=$(extract_endpoints "$SNAPSHOT")
NEW_EP=$(extract_endpoints "$WORKDIR/snapshot.json")

if [[ "$OLD_EP" == "$NEW_EP" ]]; then
  echo "No API drift detected." >&2
  exit 0
fi

echo "API DRIFT DETECTED:" >&2
diff <(echo "$OLD_EP") <(echo "$NEW_EP") >&2 || true
diff <(echo "$OLD_EP") <(echo "$NEW_EP") > drift.diff || true

# Update the snapshot in place — CI commits it as part of the drift PR.
cp "$WORKDIR/snapshot.json" "$SNAPSHOT"

exit 1
