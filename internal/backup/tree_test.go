package backup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

func TestBackupDirUsesTimeOwnTimezone(t *testing.T) {
	// A time that is 2026-03-09 in CET (UTC+1) but 2026-03-08 in UTC.
	// FormatBackupDir must NOT convert to UTC — it must use the time's own timezone.
	loc := time.FixedZone("CET", 3600)
	ts := time.Date(2026, 3, 9, 0, 30, 0, 0, loc)

	dir := backup.FormatBackupDir(ts)

	if !strings.HasPrefix(dir, "2026-03-09") {
		t.Errorf("expected dir to preserve timezone of input (2026-03-09 CET), got: %s", dir)
	}
}

func TestBuildTree_FlatPages(t *testing.T) {
	pages := []api.Page{
		{ID: "1", Title: "Root A", ParentID: ""},
		{ID: "2", Title: "Root B", ParentID: ""},
	}
	roots := backup.BuildTree(pages)
	if len(roots) != 2 {
		t.Errorf("expected 2 roots, got %d", len(roots))
	}
}

func TestBuildTree_NestedPages(t *testing.T) {
	pages := []api.Page{
		{ID: "1", Title: "Parent", ParentID: ""},
		{ID: "2", Title: "Child", ParentID: "1"},
		{ID: "3", Title: "Grandchild", ParentID: "2"},
	}
	roots := backup.BuildTree(pages)
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(roots))
	}
	if len(roots[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(roots[0].Children))
	}
	if len(roots[0].Children[0].Children) != 1 {
		t.Fatalf("expected 1 grandchild")
	}
}

func TestBuildTree_OrphansPromotedToRoot(t *testing.T) {
	// A page whose parentID doesn't exist in the set is promoted to root.
	pages := []api.Page{
		{ID: "1", Title: "Root", ParentID: ""},
		{ID: "2", Title: "Orphan", ParentID: "999"},
	}
	roots := backup.BuildTree(pages)
	if len(roots) != 2 {
		t.Errorf("expected orphan promoted to root, got %d roots", len(roots))
	}
}

func TestPageDirName_UniqueAmongSiblings(t *testing.T) {
	pages := []api.Page{
		{ID: "1", Title: "Hello World", ParentID: ""},
		{ID: "2", Title: "Hello World", ParentID: ""},
	}
	roots := backup.BuildTree(pages)
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots")
	}
	name1 := roots[0].DirName()
	name2 := roots[1].DirName()
	if name1 == name2 {
		t.Errorf("duplicate dir names for same-title pages: %q", name1)
	}
}
