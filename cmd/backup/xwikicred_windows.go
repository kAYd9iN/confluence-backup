//go:build windows

package main

import "github.com/danieljoos/wincred"

// xwikiCredentialFromStore reads the Windows Credential Manager target "xwiki-import".
func xwikiCredentialFromStore() (user, password string, ok bool) {
	cred, err := wincred.GetGenericCredential("xwiki-import")
	if err != nil || cred.UserName == "" {
		return "", "", false
	}
	p := decodeCredentialBlob(cred.CredentialBlob)
	return cred.UserName, p, p != ""
}
