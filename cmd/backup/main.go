package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "verify" {
		return runVerify(args[1:])
	}

	fs := flag.NewFlagSet("confluence-backup", flag.ExitOnError)
	domain := fs.String("domain", "", "Atlassian domain (e.g. myorg.atlassian.net)")
	output := fs.String("output", "./backups", "output directory")
	excludeRaw := fs.String("exclude-spaces", "", "comma-separated space keys to skip")
	attachments := fs.Bool("attachments", false, "download attachment files (not just metadata)")
	timeout := fs.Duration("timeout", 4*time.Hour, "overall timeout")
	dryRun := fs.Bool("dry-run", false, "fetch data but do not write files")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Parse(args) // #nosec G104 -- FlagSet uses ExitOnError; return value is unreachable

	if *showVersion {
		fmt.Println("confluence-backup", version)
		return 0
	}

	if *domain == "" {
		slog.Error("--domain is required")
		return 1
	}
	if err := api.ValidateDomain(*domain); err != nil {
		slog.Error("invalid domain", "err", err)
		return 1
	}

	creds, err := getCredentials()
	if err != nil {
		slog.Error("credentials error", "err", err)
		return 1
	}

	exclude := make(map[string]bool)
	for _, key := range strings.Split(*excludeRaw, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			exclude[key] = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var client *api.Client
	if creds.bearer {
		cloudID := creds.cloudID
		if cloudID == "" {
			discovered, err := api.DiscoverCloudID(creds.token, *domain)
			if err != nil {
				slog.Error("cloud ID discovery failed — set CONFLUENCE_CLOUD_ID to skip auto-discovery", "err", err)
				return 1
			}
			cloudID = discovered
		}
		slog.Info("using API gateway", "cloudID", cloudID)
		client = api.NewClientBearer(api.GatewayURL(cloudID), creds.token)
	} else {
		client = api.NewClient(*domain, creds.email, creds.token)
	}
	cfg := backup.Config{
		Domain:             *domain,
		OutputDir:          *output,
		ExcludeSpaces:      exclude,
		IncludeAttachments: *attachments,
		Timeout:            *timeout,
		DryRun:             *dryRun,
		ToolVersion:        version,
	}

	slog.Warn("backup output is not encrypted — ensure the output directory is stored on an encrypted volume and restricted to authorised users")
	slog.Info("starting backup", "domain", *domain, "output", *output,
		"attachments", *attachments, "dry-run", *dryRun)

	backupDir, err := backup.Run(ctx, client, cfg)
	if err != nil {
		slog.Error("backup completed with errors", "err", err)
		// Write manifest even on partial failure
		writeSignedManifest(backupDir, creds.token)
		return 1
	}

	writeSignedManifest(backupDir, creds.token)
	slog.Info("backup complete", "dir", backupDir)
	return 0
}

func writeSignedManifest(backupDir, token string) {
	if backupDir == "" {
		return
	}
	manifestPath := filepath.Join(backupDir, "backup-manifest.json")
	data, err := os.ReadFile(manifestPath) // #nosec G304 -- path is internally constructed
	if err != nil {
		slog.Warn("could not read manifest for signing", "err", err)
		return
	}
	var m backup.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		slog.Warn("could not parse manifest for signing", "err", err)
		return
	}
	if err := m.Write(manifestPath, token); err != nil {
		slog.Warn("could not sign manifest", "err", err)
	}
}
