//go:build !windows

package main

// xwikiCredentialFromStore: no credential store outside Windows; use
// XWIKI_USER / XWIKI_PASSWORD.
func xwikiCredentialFromStore() (user, password string, ok bool) { return "", "", false }
