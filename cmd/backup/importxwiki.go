package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/model"
	"github.com/kAYd9iN/confluence-backup/internal/xwiki"
)

// runImportXWiki implements:
//
//	confluence-backup import-xwiki --dir <backup-dir> --url <xwiki-url> [--parent Confluence] [--dry-run]
//
// It reads a backup and writes it into an XWiki instance via REST. It never
// contacts Confluence. XWiki credentials come from XWIKI_USER/XWIKI_PASSWORD
// or (Windows) the Credential Manager target "xwiki-import" — never from flags.
func runImportXWiki(args []string) int {
	fs := flag.NewFlagSet("import-xwiki", flag.ExitOnError)
	dir := fs.String("dir", "", "backup directory (the timestamped folder containing backup-manifest.json)")
	base := fs.String("url", "", "XWiki base URL incl. context path, e.g. https://wiki.example.com or http://localhost:8080")
	wiki := fs.String("wiki", "xwiki", "XWiki wiki ID")
	parent := fs.String("parent", "", "optional page path to import below, e.g. Confluence or Archiv/Confluence")
	dryRun := fs.Bool("dry-run", false, "show the planned page locations, write nothing")
	verify := fs.Bool("verify", true, "check afterwards that every imported page exists")
	report := fs.String("report", "xwiki-import-report.txt", "where to write the import report")
	insecure := fs.Bool("insecure-http", false, "allow sending the XWiki password over plain http to a non-local host")
	timeout := fs.Duration("timeout", 8*time.Hour, "overall timeout")
	fs.Parse(args) // #nosec G104 -- FlagSet uses ExitOnError; return value is unreachable

	if *dir == "" || (*base == "" && !*dryRun) {
		fmt.Fprintln(os.Stderr, "usage: confluence-backup import-xwiki --dir <backup-dir> --url <xwiki-url> [--parent Confluence] [--dry-run]")
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

	var c *xwiki.Client
	if *base == "" {
		*base = "http://localhost" // dry run without a target: links are planned for a root context
	}
	user, password, err := getXWikiCredentials()
	if err != nil && !*dryRun {
		slog.Error("XWiki credentials missing", "err", err)
		return 1
	}
	if c, err = xwiki.NewClient(*base, user, password); err != nil {
		slog.Error("invalid --url", "err", err)
		return 1
	}
	if c.Insecure() && !*dryRun && !*insecure {
		slog.Error("refusing to send the XWiki password over plain http to a non-local host — use https, or --insecure-http if you accept the risk")
		return 1
	}

	var parentPath []string
	for _, p := range strings.Split(*parent, "/") {
		if p = strings.TrimSpace(p); p != "" {
			parentPath = append(parentPath, p)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep, err := xwiki.Import(ctx, c, b, xwiki.Options{
		Wiki: *wiki, Parent: parentPath, DryRun: *dryRun, Verify: *verify,
		Progress: func(msg string, a ...any) { slog.Info(msg, a...) },
	})
	if werr := os.WriteFile(*report, []byte(reportText(rep, *dryRun)), 0600); werr != nil {
		slog.Warn("could not write report", "err", werr)
	}
	if err != nil {
		slog.Error("import aborted", "err", err, "report", *report)
		return 1
	}
	if *dryRun {
		slog.Info("dry run — nothing written", "planned_pages", len(rep.Plan), "report", *report)
		return 0
	}
	slog.Info("import complete", "spaces", rep.Spaces, "pages", rep.Pages, "folders", rep.Folders,
		"posts", rep.Posts, "attachments", rep.Attachments, "failed", len(rep.Failed),
		"not_found_after_import", len(rep.NotFoundAfterImport), "report", *report)
	if len(rep.Failed) > 0 || len(rep.NotFoundAfterImport) > 0 {
		return 1
	}
	return 0
}

func reportText(r xwiki.Report, dry bool) string {
	var b strings.Builder
	if dry {
		b.WriteString("XWiki-Import — Probelauf (nichts geschrieben)\n\nGeplante Seiten:\n")
		for _, p := range r.Plan {
			b.WriteString("  " + p + "\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "XWiki-Import\n\nBereiche: %d, Seiten: %d, Ordner: %d, Blogposts: %d, Anhänge: %d, Anhänge ohne Datei im Backup: %d\n",
		r.Spaces, r.Pages, r.Folders, r.Posts, r.Attachments, r.MissingFiles)
	section := func(title string, items []string) {
		fmt.Fprintf(&b, "\n%s (%d):\n", title, len(items))
		if len(items) == 0 {
			b.WriteString("  keine\n")
		}
		for _, it := range items {
			b.WriteString("  - " + it + "\n")
		}
	}
	section("Fehlgeschlagen", r.Failed)
	section("Nach dem Import nicht auffindbar", r.NotFoundAfterImport)

	b.WriteString("\nMakros ohne Entsprechung (Platzhalter, Inhalt erhalten):\n")
	keys := make([]string, 0, len(r.Placeholders))
	for k := range r.Placeholders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		b.WriteString("  keine\n")
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-30s %d\n", k, r.Placeholders[k])
	}
	b.WriteString("\nNicht auflösbare Verweise:\n")
	pages := make([]string, 0, len(r.Unresolved))
	for k := range r.Unresolved {
		pages = append(pages, k)
	}
	sort.Strings(pages)
	if len(pages) == 0 {
		b.WriteString("  keine\n")
	}
	for _, p := range pages {
		b.WriteString("  " + p + "\n")
		for _, u := range r.Unresolved[p] {
			b.WriteString("    - " + u + "\n")
		}
	}
	return b.String()
}
