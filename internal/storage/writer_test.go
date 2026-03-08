package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Getting Started", "Getting_Started"},
		{"export.pdf", "export.pdf"},
		{"../../../etc/passwd", ".._.._.._etc_passwd"},
		{"hello-world_2", "hello-world_2"},
		{"Ärger & Chaos!", "_rger___Chaos_"},
	}
	for _, c := range cases {
		got := storage.SanitizeName(c.in)
		if got != c.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWriter_WriteFile_CreatesFile(t *testing.T) {
	base := t.TempDir()
	w, err := storage.NewWriter(base)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("hello")
	if err := w.WriteFile("spaces/KB/space.json", data); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(base, "spaces/KB/space.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestWriter_WriteFile_CreatesParentDirs(t *testing.T) {
	base := t.TempDir()
	w, _ := storage.NewWriter(base)

	if err := w.WriteFile("spaces/KB/pages/Sub/index.html", []byte("<html/>")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(base, "spaces/KB/pages/Sub"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestWriter_WriteFile_Permissions(t *testing.T) {
	base := t.TempDir()
	w, _ := storage.NewWriter(base)

	if err := w.WriteFile("secret.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(base, "secret.json"))
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600, got %o", info.Mode().Perm())
	}
}

func TestWriter_WriteFile_PathTraversalBlocked(t *testing.T) {
	base := t.TempDir()
	w, _ := storage.NewWriter(base)

	err := w.WriteFile("../escape.txt", []byte("bad"))
	if err == nil {
		t.Error("expected path traversal error")
	}
	if !strings.Contains(err.Error(), "traversal") {
		t.Errorf("expected traversal error, got: %v", err)
	}
}

func TestWriter_Dir(t *testing.T) {
	base := t.TempDir()
	w, _ := storage.NewWriter(base)
	if w.Dir() != base {
		t.Errorf("Dir() = %q, want %q", w.Dir(), base)
	}
}
