package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]`)

// SanitizeName replaces unsafe characters with underscores.
// Dots are allowed for file extensions.
// Path traversal via ".." is caught by the containment check in WriteFile.
func SanitizeName(name string) string {
	return unsafeChars.ReplaceAllString(name, "_")
}

// isOutsideDir returns true when filepath.Rel indicates path escapes the base dir.
// Checks path components, not string prefix, so ".._.._foo" is not a false positive.
func isOutsideDir(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Writer writes backup files to a specific directory.
// Unlike holaspirit's Writer, this handles arbitrary subdirectory paths
// to mirror Confluence's hierarchical structure.
type Writer struct {
	dir string
}

// NewWriter creates a Writer rooted at dir. dir must already exist or be creatable.
func NewWriter(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create backup dir %s: %w", dir, err)
	}
	return &Writer{dir: dir}, nil
}

// Dir returns the root backup directory.
func (w *Writer) Dir() string { return w.dir }

// WriteFile writes data to relPath within the backup directory.
// relPath may contain subdirectories (e.g. "spaces/KB/pages/Title/index.html").
// Parent directories are created automatically with 0750 permissions.
// Path traversal attempts are blocked.
func (w *Writer) WriteFile(relPath string, data []byte) error {
	dest := filepath.Join(w.dir, relPath)

	rel, err := filepath.Rel(w.dir, dest)
	if err != nil || isOutsideDir(rel) {
		return fmt.Errorf("path traversal detected for %q", relPath)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
		return fmt.Errorf("create dir for %s: %w", relPath, err)
	}

	// Verify the resolved path is still inside the backup root (symlink protection).
	if realDir, err := filepath.EvalSymlinks(filepath.Dir(dest)); err == nil {
		if realRoot, err := filepath.EvalSymlinks(w.dir); err == nil {
			rel2, err := filepath.Rel(realRoot, realDir)
			if err != nil || isOutsideDir(rel2) {
				return fmt.Errorf("symlink escape detected for %q", relPath)
			}
		}
	}

	return os.WriteFile(dest, data, 0600)
}

// WriteBinaryStream streams r to relPath (for large attachment files).
// Caller is responsible for closing r after this call returns.
func (w *Writer) WriteBinaryStream(relPath string, r io.Reader) error {
	dest := filepath.Join(w.dir, relPath)

	rel, err := filepath.Rel(w.dir, dest)
	if err != nil || isOutsideDir(rel) {
		return fmt.Errorf("path traversal detected for %q", relPath)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
		return fmt.Errorf("create dir for %s: %w", relPath, err)
	}

	// Verify the resolved path is still inside the backup root (symlink protection).
	if realDir, err := filepath.EvalSymlinks(filepath.Dir(dest)); err == nil {
		if realRoot, err := filepath.EvalSymlinks(w.dir); err == nil {
			rel2, err := filepath.Rel(realRoot, realDir)
			if err != nil || isOutsideDir(rel2) {
				return fmt.Errorf("symlink escape detected for %q", relPath)
			}
		}
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) // #nosec G304 -- dest is validated above
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}
