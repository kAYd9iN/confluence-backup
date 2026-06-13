#!/usr/bin/env bash
# check-cbom.sh
#
# Keeps docs/cbom.cdx.json honest. Automated scanners do not detect Go stdlib
# crypto, so the CBOM is hand-authored — this script fails CI if a crypto
# package is imported in internal/ or cmd/ but is NOT represented in the CBOM,
# or if a crypto package appears that this script does not know how to map
# (forcing a conscious CBOM update before new crypto can be merged).
#
# Direction enforced: source import  ==>  CBOM asset (every crypto used must be
# declared). The reverse is intentionally not enforced: the CBOM may list
# release-side assets such as ECDSA (cosign) that are not Go imports.
#
# Exit codes: 0 = consistent, 1 = drift (missing or unknown crypto)

set -euo pipefail

CBOM="docs/cbom.cdx.json"
SRC_DIRS=("internal" "cmd")

[[ -f "$CBOM" ]] || { echo "ERROR: $CBOM not found" >&2; exit 1; }

# Map a crypto/<pkg> import to a string that MUST appear in the CBOM.
# Packages that are not cryptographic assets in their own right map to "-".
cbom_asset_for() {
	case "$1" in
		sha256) echo "SHA-256" ;;
		sha512) echo "SHA-512" ;;
		sha1)   echo "SHA-1" ;;   # present only so the policy can reject it
		md5)    echo "MD5" ;;
		hmac)   echo "HMAC-SHA-256" ;;
		tls)    echo "TLS v1.2" ;;
		aes)    echo "AES-256" ;;
		ecdsa)  echo "ECDSA" ;;
		ed25519) echo "EdDSA" ;;
		rsa)    echo "RSA" ;;
		rand|subtle) echo "-" ;;  # RNG / constant-time helpers — not assets
		*)      echo "?" ;;        # unknown — force a CBOM + script update
	esac
}

# Collect crypto/<pkg> imports from non-test source files.
mapfile -t pkgs < <(
	grep -rhoE '"crypto/[a-z0-9]+"' "${SRC_DIRS[@]}" --include='*.go' \
		--exclude='*_test.go' 2>/dev/null \
		| sed -E 's@"crypto/([a-z0-9]+)"@\1@' | sort -u
)

status=0
for pkg in "${pkgs[@]}"; do
	asset="$(cbom_asset_for "$pkg")"
	case "$asset" in
		"-")
			: ;; # non-asset crypto package, nothing to assert
		"?")
			echo "DRIFT: crypto/$pkg is imported but unknown to check-cbom.sh." >&2
			echo "       Add it to docs/cbom.cdx.json and to cbom_asset_for() in this script." >&2
			status=1 ;;
		*)
			if ! grep -q "\"$asset\"" "$CBOM"; then
				echo "DRIFT: crypto/$pkg is imported but asset '$asset' is missing from $CBOM." >&2
				status=1
			fi ;;
	esac
done

if [[ "$status" -eq 0 ]]; then
	echo "CBOM consistent: every crypto import in ${SRC_DIRS[*]} is declared in $CBOM." >&2
fi
exit "$status"
