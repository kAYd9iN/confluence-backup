package backup_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TestManifest_VerifyDetectsFileTampering is the regression for the local-test
// finding: verify must re-hash backup files, not only check the manifest's own
// signature. Modifying a backed-up file after signing must fail verification.
func TestManifest_VerifyDetectsFileTampering(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "spaces", "KB", "pages", "Home")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(nested, "index.html")
	if err := os.WriteFile(filePath, []byte("<p>original</p>"), 0600); err != nil {
		t.Fatal(err)
	}

	m := backup.NewManifest("myorg.atlassian.net", "dev", testTime())
	m.Root = dir // record relative paths
	if err := m.AddFile(filePath); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "backup-manifest.json")
	if err := m.Write(manifestPath, "tok"); err != nil {
		t.Fatal(err)
	}

	// Intact backup verifies.
	if err := backup.VerifyManifest(manifestPath, "tok"); err != nil {
		t.Fatalf("intact backup should verify: %v", err)
	}

	// Tamper with the file → verify must now fail.
	if err := os.WriteFile(filePath, []byte("<p>tampered</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	err := backup.VerifyManifest(manifestPath, "tok")
	if err == nil {
		t.Fatal("verify must fail after a backup file is modified")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("expected a hash-mismatch error, got: %v", err)
	}
}

// TestManifest_RelativePathNames confirms entries are stored relative to Root
// (forward-slashed), so nested files map uniquely.
func TestManifest_RelativePathNames(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "spaces", "KB")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(nested, "space.json")
	if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	m := backup.NewManifest("x", "dev", testTime())
	m.Root = dir
	if err := m.AddFile(p); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "backup-manifest.json")
	if err := m.Write(manifestPath, "tok"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(manifestPath)
	if !strings.Contains(string(data), `"spaces/KB/space.json"`) {
		t.Errorf("expected relative forward-slashed name in manifest, got:\n%s", data)
	}
}

func testTime() time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-03-08T12:00:00Z")
	return t
}
