package main

import (
	"strings"
	"unicode/utf16"
)

// decodeCredentialBlob converts a Windows Credential Manager blob to a string.
// `cmdkey` stores passwords as UTF-16LE, so reading the raw blob as a Go string
// leaves a NUL byte after every ASCII character — which makes an invalid HTTP
// Authorization header. Decode UTF-16LE when the blob looks like it (even
// length with embedded NULs); otherwise treat it as a plain UTF-8 string.
// Surrounding whitespace/newlines are trimmed either way.
//
// This lives in a platform-neutral file (no build tag) so it is compiled and
// unit-tested on the Linux CI, even though its only caller is Windows-only.
func decodeCredentialBlob(b []byte) string {
	hasNUL := false
	for _, c := range b {
		if c == 0 {
			hasNUL = true
			break
		}
	}
	if len(b)%2 == 0 && hasNUL {
		u16 := make([]uint16, len(b)/2)
		for i := range u16 {
			u16[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8 // little-endian
		}
		return strings.TrimSpace(string(utf16.Decode(u16)))
	}
	return strings.TrimSpace(string(b))
}
