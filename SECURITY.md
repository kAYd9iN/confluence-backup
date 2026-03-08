# Security Policy

## Unterstützte Versionen

| Version | Sicherheitsupdates |
|---------|-------------------|
| Neuestes Release | ✓ |
| Ältere Releases | — |

Nur das jeweils neueste Release erhält Sicherheitsupdates.

## Sicherheitslücke melden

**Bitte keine Sicherheitslücken als öffentliche GitHub Issues melden.**

Stattdessen bitte [GitHub Private Vulnerability Reporting](https://github.com/kAYd9iN/confluence-backup/security/advisories/new) nutzen.

### Inhalt der Meldung

- Beschreibung der Sicherheitslücke
- Schritte zur Reproduktion
- Mögliche Auswirkungen
- Optional: vorgeschlagener Fix

### Reaktionszeit

| Phase | Ziel |
|-------|------|
| Eingangsbestätigung | 5 Werktage |
| Erstbewertung | 10 Werktage |
| Fix für kritische Lücken | 30 Tage |
| Fix für mittlere/niedrige Lücken | 90 Tage |

## Scope

**In Scope:**
- Sicherheitslücken im Tool-Code (`cmd/`, `internal/`)
- Schwachstellen in CI/CD-Workflows (`.github/workflows/`)
- Token-/Authentifizierungs-Schwachstellen
- Supply-Chain-Risiken (Abhängigkeiten, Actions)

**Out of Scope:**
- Confluence-/Atlassian-API-Schwachstellen → direkt an Atlassian melden
- GitHub-Actions-Plattform-Schwachstellen → an GitHub melden
- Probleme, die physischen Zugriff auf das Backup-System erfordern

## Sicherheitsarchitektur

- GET-only HTTP-Client (kein Schreibzugriff auf Confluence)
- Bearer PAT — wird nie geloggt oder in Fehlermeldungen exponiert
- HMAC-SHA-256 Manifest-Signatur (`backup-manifest.sig`)
- Dateiberechtigungen 0600 (Dateien) / 0750 (Verzeichnisse)
- Pfad-Traversal-Schutz im Storage-Writer
- vendor/ eingecheckt (Supply-Chain)
