# confluence-backup

Automatisiertes Backup-Werkzeug für Confluence Cloud — sichert Spaces, Pages (HTML),
Blog-Posts, Kommentare, Anhänge, Templates, Benutzerprofile, Space-Berechtigungen,
Labels, Inline-Tasks, Custom Content und Smart-Content-Metadaten (Whiteboards,
Databases, Folders, Embeds) in eine hierarchische Verzeichnisstruktur mit
HMAC-SHA-256-signiertem Manifest.

## Features

- Sichert 12 Confluence-Datentypen inkl. Labels, Tasks, Custom Content und Smart-Content-Metadaten — Whiteboards, Databases, Folders, Embeds (GET-only, kein Schreibzugriff)
- SHA256-Hashes pro Datei + HMAC-SHA-256-Manifest-Signatur
- Zweistufiger Worker Pool (3 Spaces × 20 Pages global), Rate-Limiter (10 req/s)
- Hierarchische Ausgabe: `spaces/KEY/pages/Titel/Untertitel/index.html`
- Plattform-Binaries: Linux amd64/arm64, Windows amd64
- Token via Windows Credential Manager (DPAPI-geschützt) oder Umgebungsvariable
- `verify --dir <path>` — Integritätsprüfung nach dem Backup

## Installation

Binary von [GitHub Releases](https://github.com/kAYd9iN/confluence-backup/releases) herunterladen.

| Plattform | Datei |
|-----------|-------|
| Windows (64-bit) | `confluence-backup-windows-amd64.exe` |
| Linux (x86_64) | `confluence-backup-linux-amd64` |
| Linux (ARM64) | `confluence-backup-linux-arm64` |

**Integrität prüfen:**

```bash
sha256sum -c SHA256SUMS

gh attestation verify confluence-backup-linux-amd64 --repo kAYd9iN/confluence-backup

cosign verify-blob \
  --bundle confluence-backup-linux-amd64.bundle \
  confluence-backup-linux-amd64
```

## Konfiguration

**API-Token setzen:**

```bash
# Linux/macOS
export CONFLUENCE_TOKEN=<Atlassian PAT>

# Windows (Credential Manager)
cmdkey /generic:confluence-backup /user:api /pass:<Atlassian PAT>
```

**Backup ausführen:**

```bash
confluence-backup --domain myorg.atlassian.net --output ./backups
```

**Integrität prüfen:**

```bash
confluence-backup verify --dir ./backups/2026-03-08T12-00-00
```

## CLI-Referenz

```
confluence-backup [Optionen]
confluence-backup verify --dir <path>

Optionen:
  --domain DOMAIN        Atlassian-Domain (z.B. myorg.atlassian.net) [erforderlich]
  --output PATH          Backup-Zielverzeichnis (Standard: ./backups)
  --exclude-spaces KEYS  Kommagetrennte Space-Keys überspringen
  --attachments          Anhangsdateien herunterladen (nicht nur Metadaten)
  --dry-run              Verbindung testen ohne Daten zu schreiben
  --timeout DURATION     Gesamt-Timeout (Standard: 4h)
  --version              Version anzeigen
```

## Ausgabestruktur

```
backups/2026-03-08T12-00-00/
├── spaces/
│   └── KB/
│       ├── space.json           # Space-Metadaten + Berechtigungen
│       ├── labels.json          # Space- + Content-Labels
│       ├── tasks.json           # Inline-Tasks (Action Items)
│       ├── custom-content.json  # App-definierte Custom-Content-Typen
│       ├── templates/           # Space-Templates
│       ├── whiteboards/         # Whiteboard-Metadaten (pro ID)
│       ├── databases/           # Database-Metadaten (pro ID)
│       ├── folders/             # Folder-Metadaten (pro ID)
│       ├── embeds/              # Embed-Metadaten (pro ID)
│       ├── pages/
│       │   └── Getting_Started/
│       │       ├── index.html   # Page-HTML
│       │       ├── page.json    # Metadaten
│       │       ├── comments.json
│       │       └── attachments/
│       │           └── metadata.json
│       └── blog/
│           └── 2026-03-01_My_Post/
│               ├── index.html
│               └── post.json
├── users.json
├── backup-manifest.json
└── backup-manifest.sig          # HMAC-SHA-256-Signatur
```

> Whiteboards, Databases, Folders und Embeds haben keine v2-Listen-Endpoints;
> sie werden pro Space via CQL-Suche entdeckt und einzeln geladen. Die API
> liefert für diese Typen nur **Metadaten** (Canvas-/Datenbankinhalte sind
> nicht exportierbar). Dateien werden nur geschrieben, wenn Inhalt vorhanden ist.

## Security & Trust

| Maßnahme | Details |
|----------|---------|
| SLSA Level 2 | Provenance-Attestation via `actions/attest-build-provenance` |
| cosign | Keyless-Signing aller Release-Binaries via Sigstore OIDC |
| HMAC-SHA-256 | Manifest-Signatur jedes Backups (`backup-manifest.sig`) |
| GET-only API | HTTP-Client exponiert nur `Get()` + `Download()` — kein Schreibzugriff |
| TLS ≥ 1.2 | Explizit erzwungen, Zertifikatsprüfung nicht abschaltbar |
| Release-Security-Gate | Release blockiert bei offenen `security`-Issues, fehlgeschlagenem govulncheck/gosec/Race-Test oder nicht-NIST-konformer Krypto |
| CBOM + NIST-Policy | `docs/cbom.cdx.json` (CycloneDX 1.6) gegen NIST SP 800-131A via OPA/conftest geprüft |
| SHA-gepinnte Actions | Alle CI-Actions auf Commit-SHA gepinnt |
| govulncheck + gosec | SAST bei jedem Push |
| OpenSSF Scorecard | Wöchentliches Security-Scoring |
| Dependabot | 7-Tage-Cooldown + Auto-Merge reifer Minor/Patch-Bumps; Major manuell |
| Branch-Protection | `main`-Ruleset: PR + Pflicht-Checks erzwungen, kein Force-Push |
| vendor/ committed | Supply-Chain: alle Abhängigkeiten eingecheckt |

## Self-Update-Schlaufe

Ein täglicher Workflow (`api-update-check`) vergleicht Atlassians publizierte
OpenAPI-Spec gegen die committete Baseline (`docs/api-snapshot.json`) — ohne
Credentials. Bei Drift passt Claude den Code automatisch an und öffnet einen PR;
nach dem Merge erzeugt `auto-release` einen versionierten, signierten Release
(sofern das Security-Gate frei ist). Details: [CLAUDE.md](CLAUDE.md).

## Versioning

Dieses Projekt verwendet [0ver](https://0ver.org/): Major bleibt immer 0. Beispiel: v0.1.0, v0.2.0.

## Entwicklung

```bash
GOTOOLCHAIN=go1.25.8 go test -race -cover ./...
go build -mod=vendor -ldflags="-X main.version=dev" -o confluence-backup ./cmd/backup/
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
```
