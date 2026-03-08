//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/danieljoos/wincred"
)

func getToken() (string, error) {
	// Try env first; Windows Credential Manager as fallback.
	if tok := os.Getenv("CONFLUENCE_TOKEN"); tok != "" {
		return tok, nil
	}
	cred, err := wincred.GetGenericCredential("confluence-backup")
	if err != nil {
		return "", fmt.Errorf("CONFLUENCE_TOKEN not set and credential not found in Windows Credential Manager (target: confluence-backup): %w", err)
	}
	return string(cred.CredentialBlob), nil
}
