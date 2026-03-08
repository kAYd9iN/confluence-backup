//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/danieljoos/wincred"
)

func getCredentials() (email, token string, err error) {
	email = os.Getenv("CONFLUENCE_EMAIL")
	token = os.Getenv("CONFLUENCE_TOKEN")

	if email == "" {
		return "", "", fmt.Errorf("CONFLUENCE_EMAIL environment variable not set")
	}

	// Try env first; Windows Credential Manager as fallback for token.
	if token == "" {
		cred, credErr := wincred.GetGenericCredential("confluence-backup")
		if credErr != nil {
			return "", "", fmt.Errorf("CONFLUENCE_TOKEN not set and credential not found in Windows Credential Manager (target: confluence-backup): %w", credErr)
		}
		token = string(cred.CredentialBlob)
	}

	return email, token, nil
}
