package main

# NIST SP 800-131A / FIPS-approved cryptographic policy for the project CBOM
# (docs/cbom.cdx.json), evaluated with conftest (Open Policy Agent).
#
#   deny  -> FAILS the check and gates the release.
#   warn  -> informational only; does NOT fail (quantum-readiness signal).
#
# Run locally:  conftest test docs/cbom.cdx.json --policy policy

import rego.v1

# Algorithms approved for use under NIST SP 800-131A / FIPS 140-3.
approved_algorithms := {
	"SHA-256", "SHA-384", "SHA-512",
	"SHA3-256", "SHA3-384", "SHA3-512",
	"HMAC-SHA-256", "HMAC-SHA-384", "HMAC-SHA-512",
	"AES-128", "AES-192", "AES-256",
	"ECDSA", "RSA", "EdDSA",
	"ECDH",
	"ML-DSA", "ML-KEM", "SLH-DSA",
}

# Substrings that mark a definitively weak / disallowed primitive.
weak_markers := {"md5", "md4", "sha-1", "sha1", "rc4", "des", "3des", "tripledes", "blowfish", "rsa-1024"}

# Classical asymmetric algorithms: NIST-approved today, but not quantum-safe.
classical_asymmetric := {"ECDSA", "RSA", "ECDH", "DH", "DSA", "EdDSA"}

is_algorithm(c) if {
	c.type == "cryptographic-asset"
	c.cryptoProperties.assetType == "algorithm"
}

weak_named(n) if {
	some marker in weak_markers
	contains(lower(n), marker)
}

# DENY: explicitly weak algorithm anywhere in the CBOM.
deny contains msg if {
	some c in input.components
	c.type == "cryptographic-asset"
	weak_named(c.name)
	msg := sprintf("NIST policy violation: weak cryptographic algorithm %q is not permitted.", [c.name])
}

# DENY: an algorithm asset whose name is not on the approved allowlist (fail closed).
deny contains msg if {
	some c in input.components
	is_algorithm(c)
	not approved_algorithms[c.name]
	not weak_named(c.name)
	msg := sprintf("NIST policy violation: algorithm %q is not on the NIST-approved allowlist.", [c.name])
}

# DENY: TLS protocol below version 1.2.
deny contains msg if {
	some c in input.components
	c.type == "cryptographic-asset"
	c.cryptoProperties.assetType == "protocol"
	c.cryptoProperties.protocolProperties.type == "tls"
	ver := c.cryptoProperties.protocolProperties.version
	to_number(ver) < 1.2
	msg := sprintf("NIST policy violation: TLS version %s is below the required minimum of 1.2.", [ver])
}

# WARN: classical asymmetric algorithm — NIST-approved but not quantum-safe.
warn contains msg if {
	some c in input.components
	is_algorithm(c)
	classical_asymmetric[c.name]
	msg := sprintf("Informational (non-gating): %q is NIST-approved but not quantum-safe; track for post-quantum migration.", [c.name])
}
