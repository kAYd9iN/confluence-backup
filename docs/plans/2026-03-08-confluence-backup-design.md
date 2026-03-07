# Confluence Backup Tool — Design

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to create the implementation plan from this design.

**Goal:** CLI tool in Go that backs up all Confluence Cloud data (spaces, pages, blog posts, comments, attachments, templates, users, permissions) into a hierarchical, HTML-based directory structure with an HMAC-signed manifest — portable to any open-source wiki tool in an emergency.

**Architecture:** Cursor-paginated REST API client (GET-only, Bearer PAT auth), two-level bounded worker pool (space-level + page-level), hierarchical output mirroring Confluence's space/page tree, SHA256 per file + HMAC-SHA-256 manifest.

**Tech Stack:** Go 1.25.8 (go-version 1.26 in CI), `golang.org/x/time/rate` for rate limiting, `golang.org/x/sys` for Windows credentials, vendor/ checked in, 0ver versioning.

---

## Decisions

| Question | Decision | Rationale |
|----------|----------|-----------|
| Language | Go | Same as holaspirit-backup: compiled, cross-platform, no runtime |
| Auth | Bearer PAT via `CONFLUENCE_TOKEN` env var | No email needed, same UX as holaspirit |
| Content format | HTML (`body-format=view`) | Max open-source tool compatibility (BookStack, Wiki.js, Outline) |
| Output structure | Hierarchical (`spaces/KEY/pages/Title/`) | Mirrors Confluence naturally, directly navigable |
| Attachments | Default: metadata only; `--attachments` downloads files | Keeps backup small by default |
| Spaces | Default: all visible; `--exclude-spaces` filters | Cover everything, opt-out for archives |
| Page history | Current version only | Compact, sufficient for emergency access |
| Versioning | 0ver (v0.1.0, v0.2.0, ...) | Consistent with holaspirit-backup |

## What Gets Backed Up

| Resource | API | Output |
|----------|-----|--------|
| Spaces | `GET /wiki/api/v2/spaces` | `spaces/KEY/space.json` |
| Space Permissions | `GET /wiki/api/v2/spaces/{id}/permissions` | included in `space.json` |
| Space Properties | `GET /wiki/rest/api/space/{key}/property` | included in `space.json` |
| Pages | `GET /wiki/api/v2/pages?spaceId={id}&body-format=view` | `index.html` + `page.json` |
| Blog Posts | `GET /wiki/api/v2/blogposts?spaceId={id}&body-format=view` | `blog/DATE_Title/index.html` + `post.json` |
| Footer Comments | `GET /wiki/api/v2/pages/{id}/footer-comments` | `comments.json` |
| Inline Comments | `GET /wiki/api/v2/pages/{id}/inline-comments` | included in `comments.json` |
| Attachment Metadata | `GET /wiki/api/v2/pages/{id}/attachments` | `attachments/metadata.json` |
| Attachment Files | same + download URL | `attachments/files/*` (only with `--attachments`) |
| Templates | `GET /wiki/rest/api/template?spaceKey={key}` | `spaces/KEY/templates/*.json` |
| User Profiles | `GET /wiki/rest/api/user?accountId={id}` | `users.json` |

## Output Structure

```
backups/2026-03-08_120000/
  users.json
  spaces/
    KB/
      space.json
      templates/
        my-template.json
      pages/
        Getting-Started/
          index.html
          page.json
          comments.json
          attachments/
            metadata.json
            files/            ← only with --attachments
              diagram.png
          Sub-Page/
            index.html
            page.json
      blog/
        2026-03-01_Team-Update/
          index.html
          post.json
    AIAI/
      ...
  backup-manifest.json
  backup-manifest.sig
```

## API Client

- **Base URL:** `https://{domain}/wiki/api/v2/`
- **Auth:** `Authorization: Bearer <PAT>`
- **Pagination:** cursor-based (`_links.next` → `cursor` param)
- **Rate limit:** 10 req/s, burst 20 (conservative; Confluence doesn't publish limits)
- **Retry:** 429 + 5xx retryable with exponential backoff (2s, 4s, 8s); 4xx fail immediately
- **Body limit:** 100 MiB per response

## Worker Pool

- Space level: 3 concurrent spaces
- Page level: 10 concurrent pages per space, global cap 20
- Attachment downloads: separate bounded pool (5), respects `--attachments` flag

## CLI

```bash
CONFLUENCE_TOKEN=<PAT> confluence-backup \
  --domain your-org.atlassian.net \
  --output ./backups \
  [--exclude-spaces ARCHIVE,TEMP] \
  [--attachments] \
  [--timeout 4h] \
  [--dry-run]

CONFLUENCE_TOKEN=<PAT> confluence-backup verify --dir ./backups/2026-03-08_120000
confluence-backup --version
```

## Security

- GET-only client (no Post/Patch/Delete methods)
- Token never in logs, errors, or CLI flags
- File permissions: 0600 files, 0750 directories
- HMAC-SHA-256 manifest, domain-separated key (`confluence-backup-manifest-v1`)
- Path sanitizing: `[^a-zA-Z0-9_\-\.]` → `_`, `isOutsideDir()` containment check
- vendor/ checked in
- All Actions SHA-pinned
- `#nosec` only as standalone `// #nosec Gxxx -- reason`

## CI/CD

Identical to holaspirit-backup:
- `security-and-quality.yml`: govulncheck + gosec + go test -race
- `build.yml`: 3 platform binaries
- `release.yml`: GitHub Release + SLSA L2 + cosign
- `cbom.yml`: CycloneDX SBoM (`--type go`)
- `scorecard.yml`: OpenSSF Scorecard (gated on `SCORECARD_ENABLED`)
- `commit-signature.yml`: GPG commit check (gated on `COMMIT_SIGNING_ENABLED`)
- `dependency-review.yml`: block high-severity CVEs on PRs
- `api-update-check.yml`: weekly Confluence API drift detection
- `dependabot.yml`: weekly Go modules + Actions updates

## Confluence Documentation

New space `CB` (Confluence Backup) with three pages:
- Sicherheitskonzept
- Design & Architektur
- Betrieb & Installation
