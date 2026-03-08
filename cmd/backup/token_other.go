//go:build !windows

package main

import (
	"fmt"
	"os"
)

func getToken() (string, error) {
	tok := os.Getenv("CONFLUENCE_TOKEN")
	if tok == "" {
		return "", fmt.Errorf("CONFLUENCE_TOKEN environment variable not set")
	}
	return tok, nil
}
