package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "", "backup directory to verify")
	fs.Parse(args) // #nosec G104 -- FlagSet uses ExitOnError; return value is unreachable

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: confluence-backup verify --dir <backup-dir>")
		return 1
	}

	token, err := getToken()
	if err != nil {
		slog.Error("token error", "err", err)
		return 1
	}

	manifestPath := filepath.Join(*dir, "backup-manifest.json")
	if err := backup.VerifyManifest(manifestPath, token); err != nil {
		slog.Error("verification failed", "err", err)
		return 1
	}
	slog.Info("manifest verified successfully", "dir", *dir)
	return 0
}
