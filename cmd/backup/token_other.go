//go:build !windows

package main

import (
	"fmt"
	"os"
)

// credentials holds the auth configuration for the backup client.
type credentials struct {
	email   string // set for Basic Auth (personal/ATATT tokens)
	token   string
	bearer  bool   // true when using Bearer Auth (service account/ATSTT tokens)
	cloudID string // optional: Atlassian site cloud ID (skips auto-discovery)
}

func getCredentials() (credentials, error) {
	token := os.Getenv("CONFLUENCE_TOKEN")
	if token == "" {
		return credentials{}, fmt.Errorf("CONFLUENCE_TOKEN environment variable not set")
	}
	email := os.Getenv("CONFLUENCE_EMAIL")
	if email != "" {
		return credentials{email: email, token: token, bearer: false}, nil
	}
	// No email → Bearer mode (service account token via API Gateway).
	return credentials{
		token:   token,
		bearer:  true,
		cloudID: os.Getenv("CONFLUENCE_CLOUD_ID"),
	}, nil
}
