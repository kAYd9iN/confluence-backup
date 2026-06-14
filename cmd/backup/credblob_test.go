package main

import (
	"unicode/utf16"
	"testing"
)

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		b[2*i] = byte(v)
		b[2*i+1] = byte(v >> 8)
	}
	return b
}

func TestDecodeCredentialBlob(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"utf16le token (cmdkey)", utf16le("ATSTT-abc123"), "ATSTT-abc123"},
		{"utf16le with trailing newline", utf16le("ATSTT-abc123\n"), "ATSTT-abc123"},
		{"plain utf8", []byte("ATATT-xyz789"), "ATATT-xyz789"},
		{"plain utf8 with whitespace", []byte("  ATATT-xyz789\r\n"), "ATATT-xyz789"},
		{"empty", []byte{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeCredentialBlob(tt.in); got != tt.want {
				t.Errorf("decodeCredentialBlob(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecodeCredentialBlob_NoNULsLeftForHeader(t *testing.T) {
	// Regression: a UTF-16LE blob must not yield a string containing NUL bytes,
	// which made an invalid HTTP Authorization header.
	got := decodeCredentialBlob(utf16le("ATSTT-token"))
	for i := 0; i < len(got); i++ {
		if got[i] == 0 {
			t.Fatalf("decoded token still contains a NUL byte at %d", i)
		}
	}
}
