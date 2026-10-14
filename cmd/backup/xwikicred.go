package main

import (
	"fmt"
	"os"
)

// getXWikiCredentials returns the XWiki user and password for import-xwiki:
// XWIKI_USER / XWIKI_PASSWORD, or on Windows the Credential Manager target
// "xwiki-import" (user name = account, password = secret). Never from flags,
// so the password does not end up in shell history or process listings.
func getXWikiCredentials() (user, password string, err error) {
	user, password = os.Getenv("XWIKI_USER"), os.Getenv("XWIKI_PASSWORD")
	if user != "" && password != "" {
		return user, password, nil
	}
	if u, p, ok := xwikiCredentialFromStore(); ok {
		return u, p, nil
	}
	return "", "", fmt.Errorf("set XWIKI_USER and XWIKI_PASSWORD (or on Windows: cmdkey /generic:xwiki-import /user:<user> /pass:<password>)")
}
