//go:build !windows

package main

import (
	"fmt"
	"os"
)

func getCredentials() (email, token string, err error) {
	email = os.Getenv("CONFLUENCE_EMAIL")
	token = os.Getenv("CONFLUENCE_TOKEN")
	if email == "" {
		return "", "", fmt.Errorf("CONFLUENCE_EMAIL environment variable not set")
	}
	if token == "" {
		return "", "", fmt.Errorf("CONFLUENCE_TOKEN environment variable not set")
	}
	return email, token, nil
}
