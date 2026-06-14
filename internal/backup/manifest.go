package backup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FileEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Summary struct {
	TotalFiles int `json:"total_files"`
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
}

// Manifest records backup metadata and per-file integrity hashes.
// All mutating methods are safe to call from concurrent goroutines.
type Manifest struct {
	mu          sync.Mutex  // protects Files during concurrent backup
	Root        string      `json:"-"` // backup dir root; file names are stored relative to it
	Timestamp   time.Time   `json:"timestamp"`
	ToolVersion string      `json:"tool_version"`
	Domain      string      `json:"domain"`
	Files       []FileEntry `json:"files"`
	Summary     Summary     `json:"summary"`
}

func NewManifest(domain, version string, ts time.Time) *Manifest {
	return &Manifest{
		Timestamp:   ts.UTC(),
		ToolVersion: version,
		Domain:      domain,
	}
}

// hashFile returns the lowercase hex SHA-256 of the file at path. The same
// helper is used when building the manifest and when verifying it, so the two
// can never compute the hash differently.
func hashFile(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- internally constructed backup path / manifest-listed file
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// manifestName returns the name to record for a file: its path relative to the
// backup root (forward-slashed for portability), so each entry maps to a unique
// file and can be re-hashed during verify. Falls back to the base name when no
// root is set (e.g. in tests).
func (m *Manifest) manifestName(path string) string {
	if m.Root != "" {
		if rel, err := filepath.Rel(m.Root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// AddFile hashes the file at path and records it as a successful entry.
// Safe to call concurrently from multiple goroutines.
func (m *Manifest) AddFile(path string) error {
	sum, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}

	entry := FileEntry{
		Name:   m.manifestName(path),
		SHA256: sum,
		Status: "ok",
	}
	m.mu.Lock()
	m.Files = append(m.Files, entry)
	m.mu.Unlock()
	return nil
}

// AddFailedFile records an endpoint that could not be fetched or written.
// Safe to call concurrently from multiple goroutines.
func (m *Manifest) AddFailedFile(name string, err error) {
	m.mu.Lock()
	m.Files = append(m.Files, FileEntry{
		Name:   name,
		Status: "failed",
		Error:  err.Error(),
	})
	m.mu.Unlock()
}

// Write serialises the manifest and writes an HMAC-SHA-256 signature.
// Must not be called concurrently with AddFile/AddFailedFile.
func (m *Manifest) Write(path, token string) error {
	m.mu.Lock()
	m.Summary = Summary{TotalFiles: len(m.Files)}
	for _, f := range m.Files {
		if f.Status == "ok" {
			m.Summary.Successful++
		} else {
			m.Summary.Failed++
		}
	}
	m.mu.Unlock()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	sig := computeHMAC(data, token)
	sigPath := strings.TrimSuffix(path, ".json") + ".sig"
	return os.WriteFile(sigPath, []byte(sig), 0600)
}

// VerifyManifest checks the HMAC-SHA-256 signature of the manifest AND re-hashes
// every successfully-backed-up file against the signed SHA-256 in the manifest.
// The signature proves the manifest (and its hashes) is authentic; the re-hash
// proves the backup files still match it. A mismatch in either fails.
func VerifyManifest(manifestPath, token string) error {
	data, err := os.ReadFile(manifestPath) // #nosec G304 -- path comes from CLI flag
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	sigPath := strings.TrimSuffix(manifestPath, ".json") + ".sig"
	sigBytes, err := os.ReadFile(sigPath) // #nosec G304
	if err != nil {
		return fmt.Errorf("read sig: %w", err)
	}
	expectedBytes, err := hex.DecodeString(computeHMAC(data, token))
	if err != nil {
		return fmt.Errorf("compute expected signature: %w", err)
	}
	storedBytes, err := hex.DecodeString(strings.TrimSpace(string(sigBytes)))
	if err != nil {
		return fmt.Errorf("malformed signature file (%s): not valid hex", sigPath)
	}
	if !hmac.Equal(expectedBytes, storedBytes) {
		return fmt.Errorf("manifest signature mismatch — backup may have been tampered with")
	}

	// Manifest is authentic; now confirm the backup files still match it.
	var parsed struct {
		Files []FileEntry `json:"files"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	dir := filepath.Dir(manifestPath)
	checked := 0
	for _, f := range parsed.Files {
		if f.Status != "ok" {
			continue
		}
		filePath := filepath.Join(dir, filepath.FromSlash(f.Name))
		sum, err := hashFile(filePath)
		if err != nil {
			return fmt.Errorf("verify %s: %w", f.Name, err)
		}
		if !strings.EqualFold(sum, f.SHA256) {
			return fmt.Errorf("file hash mismatch for %s — backup file was modified after signing", f.Name)
		}
		checked++
	}
	_ = checked
	return nil
}

func computeHMAC(data []byte, token string) string {
	keyHash := sha256.Sum256([]byte("confluence-backup-manifest\x00" + token))
	mac := hmac.New(sha256.New, keyHash[:])
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
