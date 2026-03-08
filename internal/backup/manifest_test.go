package backup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

func TestManifest_WriteAndVerify(t *testing.T) {
	dir := t.TempDir()
	content := []byte(`{"id":"test"}`)
	path := filepath.Join(dir, "space.json")
	os.WriteFile(path, content, 0600)

	m := backup.NewManifest("myorg.atlassian.net", "dev", testTime())
	if err := m.AddFile(path); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(dir, "backup-manifest.json")
	if err := m.Write(manifestPath, "test-token"); err != nil {
		t.Fatal(err)
	}

	if err := backup.VerifyManifest(manifestPath, "test-token"); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestManifest_VerifyFailsWithWrongToken(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.json"), []byte("{}"), 0600)

	m := backup.NewManifest("myorg.atlassian.net", "dev", testTime())
	m.AddFile(filepath.Join(dir, "f.json"))
	manifestPath := filepath.Join(dir, "backup-manifest.json")
	m.Write(manifestPath, "correct-token")

	if err := backup.VerifyManifest(manifestPath, "wrong-token"); err == nil {
		t.Error("expected error with wrong token")
	}
}

func testTime() time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-03-08T12:00:00Z")
	return t
}
