# confluence-backup — v0.5.0

Backup tool for Confluence Cloud, built as a disaster-recovery source: the data must be
restorable into another wiki without Confluence. Backs up spaces, pages (current AND
archived, storage format), blog posts, comments with reply threads, attachment files,
templates, users, space permissions, labels, properties, version metadata, inline tasks,
custom content, and smart content metadata (whiteboards, databases, folders, embeds)
into a hierarchical directory structure with HMAC-SHA-256 signed manifest.

**Lossless by design**: every API object is stored as the raw JSON Confluence returned
(`Raw json.RawMessage` on the typed structs; typed fields are only what the backup logic
reads). Never reduce an object to a hand-picked struct when writing it.

## Commands

    go test ./...
    go build -o confluence-backup ./cmd/backup/

    # Service account (recommended)
    CONFLUENCE_TOKEN=<ATSTT...> CONFLUENCE_CLOUD_ID=<uuid> \
      ./confluence-backup --domain myorg.atlassian.net --output ./backups

    # Personal account
    CONFLUENCE_EMAIL=user@example.com CONFLUENCE_TOKEN=<ATATT...> \
      ./confluence-backup --domain myorg.atlassian.net --output ./backups

    ./confluence-backup verify --dir ./backups/2026-03-09T08-00-00

## Key Files

| Path | Purpose |
|------|---------|
| `internal/api/client.go` | GET-only HTTP client, Basic Auth (ATATT) or Bearer (ATSTT), rate 10 req/s |
| `internal/api/discovery.go` | DiscoverCloudID via accessible-resources; GatewayURL() |
| `internal/api/pagination.go` | Cursor-based pagination (Confluence v2) |
| `internal/api/confluence.go` | Resource types + fetch functions, space-scoped endpoints |
| `internal/backup/tree.go` | Builds page hierarchy from flat API list |
| `internal/backup/backup.go` | Orchestration, two-level worker pool (3 spaces × cap 20 pages) |
| `internal/backup/manifest.go` | SHA256 per file + HMAC-SHA-256 .sig, sync.Mutex protected |
| `internal/storage/writer.go` | Hierarchical writer, 0600 files, path-traversal + symlink protection |
| `cmd/backup/main.go` | CLI entry point (`verify`, `export` subcommands) |
| `internal/model/model.go` | Loads a backup dir: spaces, page tree from parentId (incl. folders), posts, attachments, comments, users |
| `internal/convert/convert.go` | Confluence storage format → clean, well-formed, sanitized XHTML; links/images via a `Resolver`; reports unresolved refs + macro placeholders |
| `internal/export/export.go` | Static HTML site from a loaded backup (disaster-recovery read access) |
| `internal/xwiki/` | XWiki REST client + importer (`import-xwiki`): writes ONLY to the given XWiki, never to Confluence |
| `scripts/check-api-schema.sh` | Credential-free API drift check vs published OpenAPI spec |
| `scripts/check-cbom.sh` | Anti-staleness: every crypto import must be in the CBOM |
| `docs/cbom.cdx.json` | CycloneDX 1.6 Cryptography BoM (hand-authored) |
| `policy/nist-crypto.rego` | OPA/conftest NIST crypto policy (gates the release) |

## Architecture

- **GET-only**: Client has only Get() + Download() — no write methods
- **Auth auto-detect**:
  - `CONFLUENCE_EMAIL` set → Basic Auth (`ATATT` token) against `https://{domain}/wiki/...`
  - No email → Bearer Auth (`ATSTT` service account token) via API Gateway:
    `https://api.atlassian.com/ex/confluence/{cloudID}/wiki/...`
  - Service-account tokens need granular Confluence read scopes
    (read:space/page/blogpost/space.permission/space.property/label/
    custom-content/template/attachment/comment/task/whiteboard/database/
    folder/embed/user:confluence + search:confluence) or the API returns
    `401 "scope does not match"` — full list in README → Konfiguration
  - Windows credential blob is UTF-16LE (cmdkey) — decoded in `cmd/backup/credblob.go`
- **Cloud ID**: set `CONFLUENCE_CLOUD_ID` to skip auto-discovery (recommended; auto-discovery requires `read:me` scope)
- **Space-scoped endpoints**: `/wiki/api/v2/spaces/{id}/pages` — avoids API Gateway filter bug
- **Body format**: `storage` (Confluence native XML) — `view` not supported by API Gateway
- **Pagination**: v2 follows `_links.next` (cursor); v1 (`FetchAllV1`: CQL search,
  templates) also follows `_links.next` — the server caps `limit` ("fixed system
  limits"), so a short page is NOT the end — with an offset fallback via `start`.
  Next links: only path+query are used, never a host; must stay under /wiki/rest/api/
- **Comments**: v2 lists return ROOT comments only; replies come from
  `/{footer|inline}-comments/{id}/children`, nested under `"replies"`
- **Failures are never silent**: everything goes through the `recorder` in
  internal/backup/backup.go; a fetch/write/hash failure becomes a `failed` manifest
  entry and `Run` returns an error (exit code 1)
- **Two-level pool**: 3 concurrent spaces, 20 concurrent pages (global cap)
- **Hierarchical output**: `spaces/KEY/pages/Title/SubTitle/index.html`
- **Backup dir timestamp**: local timezone (not UTC)
- **Attachments**: metadata + files by default (`--attachments=false`: metadata only);
  download via the documented v1 endpoint `AttachmentDownloadPath` (works with Basic and
  gateway Bearer auth), falling back to `/wiki` + `downloadLink`. Separate HTTP client
  without overall timeout (large files); redirects only same-host or https *.atlassian.com/.net.
  Stored as `attachments/files/<attachmentId>_<title>` (ID prevents collisions)
- **Smart content** (whiteboards, databases, folders, embeds): no v2 list
  endpoints — discovered per space via v1 CQL search, fetched by ID;
  **metadata only** (canvas/database contents are not exportable via API)
- **Labels/tasks/custom content**: per space as labels.json, tasks.json,
  custom-content.json (only written when non-empty)
- **HMAC key**: domain-separated (confluence-backup-manifest\x00)
- **TLS**: explicit minimum TLS 1.2, `InsecureSkipVerify` hard-false (shared `newHTTPClient()`)
- **CBOM**: real crypto surface in `docs/cbom.cdx.json`, checked against NIST
  SP 800-131A via OPA/conftest; non-approved algorithm or TLS<1.2 gates the release
- **vendor/**: checked in for supply-chain safety

## Credentials

| Env var | Required | Description |
|---------|----------|-------------|
| `CONFLUENCE_TOKEN` | yes | API token (`ATATT` for personal, `ATSTT` for service account) |
| `CONFLUENCE_EMAIL` | for Basic Auth | Personal account email — omit for Bearer/service account mode |
| `CONFLUENCE_CLOUD_ID` | recommended | Atlassian site UUID — skips auto-discovery |
| `CONFLUENCE_DOMAIN` | via `--domain` flag | e.g. `myorg.atlassian.net` |

## Repo

- GitHub: kAYd9iN/confluence-backup (public)
- Versioning: 0ver — v0.1.0, v0.2.0, ... (https://0ver.org/)
- go.mod: `go 1.26.9` (minimum patched toolchain). CI uses `go-version: '1.26'` +
  `check-latest: true`, so every run gets the newest 1.26 patch — without it setup-go
  uses the runner's cached Go, which made govulncheck fail (GO-2026-6617, GO-2026-6613)

## Self-Update Loop (API drift)

```
api-update-check (daily 06:00 UTC — no credentials needed)
  → compares Atlassian's PUBLISHED OpenAPI specs against docs/api-snapshot.json
  → drift detected → job `check`: Claude adapts internal/api/confluence.go
    (needs ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN) → patch artifact
  → job `verify`: build + race tests on the patch (read-only, no secrets)
  → job `pr`: PR with label api-drift (auto-merge only if snapshot-only;
    Go code changes always require human review — prompt-injection guard)
  → merge → auto-release bumps 0ver minor + pushes tag
  → release workflow: security-gate (govulncheck, gosec, race tests, CBOM NIST
    policy, blocks while issues labeled `security` are open) → build → signed release
```

- The check uses Atlassian's public spec (dac-static.atlassian.com) — the API
  definition is identical for every Confluence Cloud instance, so no instance,
  domain, or token is required
- Baseline: `docs/api-snapshot.json` (committed); sentinel `__ENDPOINT_MISSING__`
  marks endpoints the tool calls that are no longer documented (currently: none)
- Exit 2 (spec download failed) fails the job; no snapshot is written
- Trust split: `drift.diff` comes from Atlassian's public specs (untrusted). The
  `check` job has no persisted git credentials, and Claude is limited to Read/Edit/
  Write/Glob/Grep + `go build`/`go vet` (no `go test`, it executes code). Tests run
  in `verify` (no secrets, no OIDC, no Go cache). Only `pr` sees REPO_PAT, and it
  runs no patch code. Only `docs/api-snapshot.json` and `internal/` enter the patch
- One drift PR at a time: while an `api-drift` PR is open, the check only reports

## Dependency Updates (Dependabot)

- `dependabot.yml`: daily, with a 7-day `cooldown` (supply-chain maturity window
  — a release is only proposed a week after publication; security advisories bypass it)
- `dependabot-auto-merge.yml`: auto-merges non-major bumps once required CI is green
  (build + security-and-quality + dependency-review); major bumps stay open for review
- Branch ruleset `main-protection` enforces PR + required checks + **squash-only
  merges** before any merge

## Commit signing — not enforced (by design)

Commit signing is intentionally NOT enforced at the branch level. GitHub's
`required_signatures` rule rejects merging any PR that contains unsigned commits
(it inspects the PR's commits, not just the squash result) — which would break
the api-drift self-update loop (its `github-actions[bot]` commits are unsigned)
and block any human PR with unsigned commits. Enforcing it would require every
commit producer to sign: local commits (SSH/GPG signing) AND reworking the loop
to create its commits via the GitHub API. The high-value signature — the release
artifacts — is already provided by cosign + SLSA provenance, so commit signing
would be defense-in-depth only. The old `commit-signature.yml` workflow was
removed: its strict mode was incompatible with bot commits and its warn-only mode
verified nothing.

## Configured (was "pending")

- ✅ Repo variable SCORECARD_ENABLED=true; secrets CLAUDE_CODE_OAUTH_TOKEN + REPO_PAT set
- ✅ "Allow auto-merge" enabled; `main-protection` ruleset active (PR + checks +
  squash-only); Actions cannot approve PRs
- Optional, still open: SCORECARD_TOKEN (improves Branch-Protection check only)

## Disaster recovery: export / import

- `convert` must never drop content silently: macros without an equivalent keep their
  body and get a visible placeholder; both placeholders and unresolved links are reported.
  Output must stay well-formed XML (XWiki imports it as xhtml/1.0) — a test parses it strictly
- `encoding/xml` (non-strict, HTML entities) parses storage format; do NOT use
  `xml.HTMLAutoClose` — it lists "link"/"param" and AutoClose matches local names, so
  `<ac:link>` would become void
- The pipeline test `TestPipeline_BackupThenExport` (internal/backup) runs fake Confluence →
  backup → model.Load → export; `CONFLUENCE_EXPORT_PREVIEW=<dir>` keeps the site for a look
- XWiki import (`TestPipeline_BackupThenXWikiImport` against a fake REST API): space homepage →
  space WebHome, every page non-terminal (`.../WebHome`), content in `xhtml/1.0`, comments
  appended to the content (XWiki comment objects would need xwiki/2.1 escaping), origin note
  with author/date (REST sets the importing user). Idempotent PUTs. Credentials only via
  XWIKI_USER/XWIKI_PASSWORD or Windows target `xwiki-import`; 401/403 aborts immediately
- Learned against a real XWiki 17.10 (not visible with the fake API):
  - xhtml/1.0 content must be a full document with ONE root element — `putPage` wraps it
    in `<html xmlns=…><body>…</body></html>`, otherwise rendering fails
  - `<img src>` without a scheme is read as an ATTACHMENT NAME of the current page, so
    images use XWiki attachment references (`Resolver.Image`: `file` or
    `Space.Page.WebHome@file`, escaped), while links keep `/bin/view|download/…` URLs
- `TestXWikiImport_Live` (opt-in: XWIKI_TEST_URL, XWIKI_USER, XWIKI_PASSWORD) imports twice
  into a real XWiki and checks storage, attachment, tags and the rendered page. Quick setup:
  `xwiki:lts-postgres-tomcat` + `postgres` via compose, enable `xwiki.superadminpassword`
  in WEB-INF/xwiki.cfg (the REST API works before the distribution wizard)

## Extending: Adding a New Data Type

1. Add fetch function to internal/api/confluence.go (use space-scoped endpoint);
   return the raw JSON objects
2. Call it in internal/backup/backup.go (processSpace / writeContent) and write
   through the `recorder` (`rec.writeJSON` / `rec.fail`) — never just `slog.Warn`
3. Add the endpoint to `ENDPOINTS` in scripts/check-api-schema.sh and regenerate
   docs/api-snapshot.json by running the script
4. Extend the fake server in internal/backup/backup_test.go and run go test ./...
5. Commit
