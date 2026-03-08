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

// AddFile hashes the file at path and records it as a successful entry.
// Safe to call concurrently from multiple goroutines.
func (m *Manifest) AddFile(path string) error {
	f, err := os.Open(path) // #nosec G304 -- path is always an internally constructed backup path
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}

	entry := FileEntry{
		Name:   filepath.Base(path),
		SHA256: hex.EncodeToString(h.Sum(nil)),
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
	expected := computeHMAC(data, token)
	if !hmac.Equal([]byte(expected), sigBytes) {
		return fmt.Errorf("manifest signature mismatch — backup may have been tampered with")
	}
	return nil
}

func computeHMAC(data []byte, token string) string {
	keyHash := sha256.Sum256([]byte("confluence-backup-manifest\x00" + token))
	mac := hmac.New(sha256.New, keyHash[:])
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
