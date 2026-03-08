# confluence-backup — v0.1.0 RELEASED (2026-03-08)

Backup tool for Confluence Cloud. Backs up spaces, pages (HTML), blog posts,
comments, attachments, templates, users, and space permissions into a
hierarchical directory structure with HMAC-SHA-256 signed manifest.

## Commands

    GOTOOLCHAIN=go1.25.8 go test ./...
    go build -o confluence-backup ./cmd/backup/
    CONFLUENCE_TOKEN=<PAT> ./confluence-backup --domain myorg.atlassian.net --output ./backups
    CONFLUENCE_TOKEN=<PAT> ./confluence-backup verify --dir ./backups/2026-03-08T12-00-00

## Key Files

| Path | Purpose |
|------|---------|
| `internal/api/client.go` | GET-only HTTP client, rate 10 req/s, retry 429+5xx |
| `internal/api/pagination.go` | Cursor-based pagination (Confluence v2) |
| `internal/api/confluence.go` | Resource types + fetch functions for all 8 data types |
| `internal/backup/tree.go` | Builds page hierarchy from flat API list |
| `internal/backup/backup.go` | Orchestration, two-level worker pool (3 spaces × cap 20 pages) |
| `internal/backup/manifest.go` | SHA256 per file + HMAC-SHA-256 .sig |
| `internal/storage/writer.go` | Hierarchical writer, 0600 files, path-traversal protection |
| `cmd/backup/main.go` | CLI entry point |

## Architecture

- **GET-only**: Client has only Get() + Download() — no write methods
- **Bearer PAT**: token via CONFLUENCE_TOKEN env var or Windows Credential Manager
- **Cursor pagination**: follows _links.next until exhausted
- **Two-level pool**: 3 concurrent spaces, 20 concurrent pages (global cap)
- **Hierarchical output**: spaces/KEY/pages/Title/SubTitle/index.html
- **Attachments**: metadata always; files only with --attachments flag
- **HMAC key**: domain-separated (confluence-backup-manifest\x00)
- **vendor/**: checked in for supply-chain safety

## Repo

- GitHub: kAYd9iN/confluence-backup (public)
- Versioning: 0ver — v0.1.0, v0.2.0, ... (https://0ver.org/)
- go.mod: go 1.25.8 / CI go-version: '1.26' — do not change

## Pending Manual Steps (after first push)

- Set SCORECARD_TOKEN secret (PAT with repo + read:org)
- Set COMMIT_SIGNING_PUBLIC_KEY secret (GPG key)
- Set CONFLUENCE_TOKEN secret + CONFLUENCE_DOMAIN variable for api-update-check workflow

## Extending: Adding a New Data Type

1. Add fetch function to internal/api/confluence.go
2. Call it in internal/backup/backup.go (processSpace or Run)
3. Run GOTOOLCHAIN=go1.25.8 go test ./... to verify
4. Commit
