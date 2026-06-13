# Security Policy

## Supported Versions

Only the latest release receives security updates.

| Version | Security updates |
|---------|-----------------|
| Latest release | ✓ |
| Older releases | — |

## Reporting a Vulnerability

**Please do not report security vulnerabilities as public GitHub Issues.**

Use [GitHub Private Vulnerability Reporting](https://github.com/kAYd9iN/confluence-backup/security/advisories/new) instead.

### Report contents

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Optional: suggested fix

### Response times

| Phase | Target |
|-------|--------|
| Acknowledgement | 5 business days |
| Initial assessment | 10 business days |
| Fix for critical issues | 30 days |
| Fix for medium/low issues | 90 days |

## Scope

**In scope:**
- Vulnerabilities in tool code (`cmd/`, `internal/`)
- Weaknesses in CI/CD workflows (`.github/workflows/`)
- Token / authentication vulnerabilities
- Supply-chain risks (dependencies, Actions)

**Out of scope:**
- Confluence / Atlassian API vulnerabilities → report directly to Atlassian
- GitHub Actions platform vulnerabilities → report to GitHub
- Issues requiring physical access to the backup system

## Security Architecture

- **GET-only HTTP client** — no write access to Confluence possible
- **Bearer PAT** — never logged or included in error messages
- **HMAC-SHA-256 manifest signature** (`backup-manifest.sig`) — detects tampering
- **File permissions** 0600 (files) / 0750 (directories)
- **Path-traversal protection** in the storage writer (`isOutsideDir`)
- **SSRF protection** — `ValidateDomain` blocks private/loopback ranges; `Download()` validates URL host against client domain
- **vendor/ checked in** — reproducible builds, no network required

## Backup Data Security

Backup output is **not encrypted at rest**. The backup directory contains
plaintext HTML and JSON of all Confluence content.

**Recommended mitigations:**

1. Set restrictive filesystem permissions on the output directory (`chmod 700`)
2. Store backups on an encrypted volume (LUKS, FileVault, BitLocker)
3. Restrict access via OS-level access controls
4. Consider encrypting with `age` or `gpg` before transferring off-site

## Token Rotation

The HMAC-SHA-256 manifest signature is derived from the Confluence API token.
**If the token is rotated, existing manifest signatures become unverifiable.**

Document the token in use when a backup was created (the token itself is not
stored — only its derived HMAC key is used for signing).

## Backup Retention

No retention policy is enforced by the tool. Operators should:

1. Define a retention period appropriate for their compliance requirements
2. Securely delete backups beyond the retention period (`shred`, encrypted delete)
3. Audit who has access to the backup storage location

## Cryptography Bill of Materials (CBOM)

The tool's cryptographic surface is enumerated in a CycloneDX 1.6 CBOM at
[`docs/cbom.cdx.json`](docs/cbom.cdx.json): SHA-256 (hashing), HMAC-SHA-256
(manifest signature), TLS ≥ 1.2 (transport), and ECDSA P-256 (cosign release
signing). Automated scanners do not detect Go stdlib crypto, so the CBOM is
hand-authored and kept honest by `scripts/check-cbom.sh`, which fails CI if a
crypto import is not declared in it.

The CBOM is checked against a **NIST SP 800-131A / FIPS-approved** policy
([`policy/nist-crypto.rego`](policy/nist-crypto.rego), Open Policy Agent /
conftest). A non-approved algorithm (e.g. MD5, SHA-1, RC4, or TLS < 1.2) fails
the **release security gate** and blocks the release. Quantum-readiness is
reported as a non-gating warning: ECDSA P-256 is NIST-approved but not
quantum-safe, tracked for future post-quantum migration.
