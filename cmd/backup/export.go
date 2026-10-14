package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kAYd9iN/confluence-backup/internal/export"
	"github.com/kAYd9iN/confluence-backup/internal/model"
)

// runExport implements: confluence-backup export --dir <backup-dir> --out <site-dir>
// It reads the backup only (no network, no credentials) and writes a static
// HTML site that can be opened in any browser.
func runExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dir := fs.String("dir", "", "backup directory (the timestamped folder containing backup-manifest.json)")
	out := fs.String("out", "", "output directory for the static HTML site")
	fs.Parse(args) // #nosec G104 -- FlagSet uses ExitOnError; return value is unreachable

	if *dir == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: confluence-backup export --dir <backup-dir> --out <site-dir>")
		return 1
	}
	if _, err := os.Stat(filepath.Join(*dir, "backup-manifest.json")); err != nil {
		slog.Error("not a backup directory (backup-manifest.json missing)", "dir", *dir)
		return 1
	}

	b, err := model.Load(*dir)
	if err != nil {
		slog.Error("loading backup failed", "err", err)
		return 1
	}
	report, err := export.Export(b, *out)
	if err != nil {
		slog.Error("export failed", "err", err)
		return 1
	}
	unresolved := 0
	for _, refs := range report.Unresolved {
		unresolved += len(refs)
	}
	slog.Info("export complete",
		"site", filepath.Join(*out, "index.html"),
		"pages", report.Pages, "posts", report.Posts, "attachments", report.Attachments,
		"unresolved_links", unresolved, "missing_files", report.MissingFiles)
	slog.Info("details on placeholders and unresolved links", "report", filepath.Join(*out, "export-report.txt"))
	return 0
}
