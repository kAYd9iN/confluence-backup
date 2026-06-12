# confluence-backup — v0.3.0 (2026-03-09)

Backup tool for Confluence Cloud. Backs up spaces, pages (HTML storage format), blog posts,
comments, attachments, templates, users, and space permissions into a
hierarchical directory structure with HMAC-SHA-256 signed manifest.

## Commands

    GOTOOLCHAIN=go1.25.8 go test ./...
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
| `cmd/backup/main.go` | CLI entry point |

## Architecture

- **GET-only**: Client has only Get() + Download() — no write methods
- **Auth auto-detect**:
  - `CONFLUENCE_EMAIL` set → Basic Auth (`ATATT` token) against `https://{domain}/wiki/...`
  - No email → Bearer Auth (`ATSTT` service account token) via API Gateway:
    `https://api.atlassian.com/ex/confluence/{cloudID}/wiki/...`
- **Cloud ID**: set `CONFLUENCE_CLOUD_ID` to skip auto-discovery (recommended; auto-discovery requires `read:me` scope)
- **Space-scoped endpoints**: `/wiki/api/v2/spaces/{id}/pages` — avoids API Gateway filter bug
- **Body format**: `storage` (Confluence native XML) — `view` not supported by API Gateway
- **Cursor pagination**: follows _links.next until exhausted
- **Two-level pool**: 3 concurrent spaces, 20 concurrent pages (global cap)
- **Hierarchical output**: `spaces/KEY/pages/Title/SubTitle/index.html`
- **Backup dir timestamp**: local timezone (not UTC)
- **Attachments**: metadata always; files only with --attachments flag
- **HMAC key**: domain-separated (confluence-backup-manifest\x00)
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
- go.mod: go 1.25.8 / CI go-version: '1.26' — do not change

## Self-Update Loop (API drift)

```
api-update-check (daily 06:00 UTC)
  → drift detected → Claude adapts internal/api/confluence.go (needs ANTHROPIC_API_KEY)
  → PR with label api-drift (auto-merge only if snapshot-only;
    Go code changes always require human review — prompt-injection guard)
  → merge → auto-release bumps 0ver minor + pushes tag
  → release workflow: security-gate (govulncheck, gosec, race tests,
    blocks while issues labeled `security` are open) → build → signed release
```

- Baseline: `docs/api-snapshot.json` — committed via PR on first successful run
- Script supports Basic Auth (set `CI_CONFLUENCE_EMAIL` secret for ATATT tokens) or Bearer (ATSTT)
- Domain/token/email are **secrets**, not variables — repo is public, secrets are masked in logs
- If neither CONFLUENCE_DOMAIN nor CI_CONFLUENCE_TOKEN secret is set, the job skips gracefully
- Exit 2 (all endpoints unreachable) fails the job instead of writing an empty baseline

## Pending Manual Steps

- Set SCORECARD_TOKEN secret (optional — only improves Branch-Protection check)
- Set COMMIT_SIGNING_PUBLIC_KEY secret (GPG key)
- Set CONFLUENCE_DOMAIN secret (e.g. myorg.atlassian.net — for the daily API drift check)
- Set CI_CONFLUENCE_TOKEN secret (read-only token for the daily API drift check)
- Set CI_CONFLUENCE_EMAIL secret (only for ATATT personal tokens — Basic Auth)
- Set ANTHROPIC_API_KEY secret **or** CLAUDE_CODE_OAUTH_TOKEN secret (Pro/Max
  subscription via `claude setup-token`) — enables automatic code adaptation on drift
- Set REPO_PAT secret (PAT with repo + workflow scope — lets drift PRs trigger CI
  and auto-release tags trigger the release workflow)
- Enable "Allow auto-merge" in repo settings (snapshot-only drift PRs merge once CI is green)

## Extending: Adding a New Data Type

1. Add fetch function to internal/api/confluence.go (use space-scoped endpoint)
2. Call it in internal/backup/backup.go (processSpace or Run)
3. Run GOTOOLCHAIN=go1.25.8 go test ./... to verify
4. Commit
