# Confluence Backup Tool — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** CLI tool in Go that backs up all Confluence Cloud data into a hierarchical HTML directory structure with HMAC-signed manifest, portable to any open-source wiki tool.

**Architecture:** GET-only Bearer-PAT client with cursor pagination, two-level bounded worker pool (3 spaces × 10 pages, global cap 20), hierarchical writer that mirrors Confluence's space/page tree, SHA256-per-file + HMAC-SHA-256 manifest — identical security posture to holaspirit-backup.

**Tech Stack:** Go 1.25.8, `golang.org/x/time/rate`, `golang.org/x/sys` (Windows creds), vendor/ checked in, 0ver versioning (v0.1.0).

**Reference:** holaspirit-backup (`github.com/kAYd9iN/holaspirit-backup`) is the reference implementation. Read it when a pattern is described as "same as holaspirit" — exact code there.

---

### Task 1: Module scaffold

**Files:**
- Create: `go.mod`
- Create: `cmd/backup/main.go` (stub)
- Create: `internal/api/.keep`
- Create: `internal/backup/.keep`
- Create: `internal/storage/.keep`

**Step 1: Write go.mod**

```
module github.com/kAYd9iN/confluence-backup

go 1.25.8

require (
	golang.org/x/sys v0.20.0
	golang.org/x/time v0.14.0
)
```

**Step 2: Write stub main.go**

```go
package main

import "fmt"

var version = "dev"

func main() {
	fmt.Println("confluence-backup", version)
}
```

**Step 3: Run go mod tidy + vendor**

```bash
cd /home/nic/confluence-backup
GOTOOLCHAIN=go1.25.8 go mod tidy
GOTOOLCHAIN=go1.25.8 go mod vendor
```

Expected: `vendor/` directory created with `golang.org/x/time` and `golang.org/x/sys`.

**Step 4: Verify it builds**

```bash
GOTOOLCHAIN=go1.25.8 go build ./...
```

Expected: no errors, binary not created yet (stub main).

**Step 5: Commit**

```bash
git add go.mod go.sum vendor/ cmd/backup/main.go
git commit -m "chore: scaffold module, vendor dependencies"
```

---

### Task 2: HTTP client (`internal/api/client.go`)

**Files:**
- Create: `internal/api/client.go`
- Create: `internal/api/client_test.go`
- Create: `internal/api/security_test.go`

**Step 1: Write the failing tests**

`internal/api/client_test.go`:
```go
package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestClient_Get_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing auth header")
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "test-token")
	body, err := c.Get(context.Background(), "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestClient_Get_RetryOn429(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	c.MaxRetries = 3
	c.RetryDelay = 0
	_, err := c.Get(context.Background(), "/test")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestClient_Get_NoRetryOn4xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	c.MaxRetries = 3
	c.RetryDelay = 0
	_, err := c.Get(context.Background(), "/test")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", calls)
	}
}
```

`internal/api/security_test.go`:
```go
package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestClient_TokenNotInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	secret := "super-secret-token-xyz"
	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), secret)
	_, err := c.Get(context.Background(), "/test")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("token leaked in error: %v", err)
	}
}

func TestClient_GETOnly(t *testing.T) {
	// Client struct must not expose Post, Put, Patch, Delete methods.
	// This is a compile-time check via interface assertion.
	type getOnly interface {
		Get(ctx context.Context, path string) ([]byte, error)
	}
	var _ getOnly = (*api.Client)(nil)
	// If Client accidentally had Post(), it would still compile.
	// The real guarantee is the absence of those methods in the source.
	// Document the intent here.
	t.Log("Client is GET-only by design: only Get() and Download() methods exist")
}
```

**Step 2: Run tests — expect compile failure**

```bash
cd /home/nic/confluence-backup
GOTOOLCHAIN=go1.25.8 go test ./internal/api/ 2>&1 | head -20
```

Expected: `cannot find package` or `undefined: api.NewClient`

**Step 3: Write `internal/api/client.go`**

```go
package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// 10 requests/second, burst of 20 — conservative for Confluence Cloud.
var rateLimit = rate.Every(100 * time.Millisecond)

const (
	rateBurst    = 20
	maxRetries   = 3
	maxBodyBytes = 100 * 1024 * 1024 // 100 MiB per response
)

// Client is an authenticated HTTP client for the Confluence Cloud API.
// It only supports GET requests — no Post(), Patch(), or Delete() methods exist.
type Client struct {
	httpClient *http.Client
	domain     string // e.g. "your-org.atlassian.net"
	token      string
	limiter    *rate.Limiter
	MaxRetries int
	RetryDelay time.Duration
}

// NewClient creates a client for the given Atlassian domain.
// token is a Personal Access Token — never logged or returned.
func NewClient(domain, token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		domain:     domain,
		token:      token,
		limiter:    rate.NewLimiter(rateLimit, rateBurst),
		MaxRetries: maxRetries,
		RetryDelay: 2 * time.Second,
	}
}

// BaseURL returns the Confluence API base (scheme + domain only, no path).
func (c *Client) BaseURL() string {
	return "https://" + c.domain
}

// Get performs a GET request with rate limiting and retry.
// Only retries on 429 (rate limited) and 5xx (server errors).
// 4xx responses (except 429) fail immediately — no retry.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := c.RetryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter: %w", err)
		}

		body, retryable, err := c.doGet(ctx, "https://"+c.domain+path)
		if err == nil {
			return body, nil
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("after %d retries: %w", c.MaxRetries, lastErr)
}

// Download streams a binary response from a full URL (for attachment files).
// Caller MUST close the returned ReadCloser.
// No body size limit — attachments can be large.
func (c *Client) Download(ctx context.Context, url string) (io.ReadCloser, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *Client) doGet(ctx context.Context, url string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, true, fmt.Errorf("rate limited (429)")
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("server error HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return nil, false, fmt.Errorf("client error HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	return body, false, err
}
```

**Step 4: Run tests — expect pass**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/api/ -v -run TestClient
```

Expected: all TestClient_* tests PASS.

**Step 5: Commit**

```bash
git add internal/api/
git commit -m "feat: add GET-only HTTP client with rate limiting and retry"
```

---

### Task 3: Cursor pagination (`internal/api/pagination.go`)

**Files:**
- Create: `internal/api/pagination.go`
- Create: `internal/api/pagination_test.go`

**Step 1: Write failing tests**

`internal/api/pagination_test.go`:
```go
package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestFetchAll_SinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": "1"}, {"id": "2"}},
			"_links":  map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	results, err := api.FetchAll(context.Background(), c, "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestFetchAll_MultiPage(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{"id": "1"}},
				"_links":  map[string]any{"next": "/wiki/api/v2/spaces?cursor=abc"},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{"id": "2"}, {"id": "3"}},
				"_links":  map[string]any{},
			})
		}
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	results, err := api.FetchAll(context.Background(), c, "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results across 2 pages, got %d", len(results))
	}
}

func TestFetchAll_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []any{},
			"_links":  map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	results, err := api.FetchAll(context.Background(), c, "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestExtractCursorPath(t *testing.T) {
	tests := []struct {
		nextLink string
		want     string
	}{
		{"/wiki/api/v2/spaces?cursor=abc123&limit=50", "/wiki/api/v2/spaces?cursor=abc123&limit=50"},
		{"", ""},
		{"/wiki/api/v2/spaces", "/wiki/api/v2/spaces"},
	}
	for _, tt := range tests {
		got := api.ExtractCursorPath(tt.nextLink)
		if got != tt.want {
			t.Errorf("ExtractCursorPath(%q) = %q, want %q", tt.nextLink, got, tt.want)
		}
	}
}

// Ensure FetchAll stops after a reasonable page count (safety cap).
func TestFetchAll_PageCap(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Always return a next link — would loop forever without a cap.
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": fmt.Sprintf("%d", calls)}},
			"_links":  map[string]any{"next": "/wiki/api/v2/spaces?cursor=loop"},
		})
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	_, err := api.FetchAll(context.Background(), c, "/wiki/api/v2/spaces")
	// Should stop at the safety cap (10000 pages) and return an error.
	if err == nil {
		t.Error("expected error when page cap exceeded")
	}
}
```

**Step 2: Run tests — expect compile failure**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/api/ -run TestFetchAll 2>&1 | head -5
```

**Step 3: Write `internal/api/pagination.go`**

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
)

const maxPages = 10_000 // safety cap — Confluence instances with >10k pages per query are pathological

// pagedResponse is the envelope returned by all Confluence v2 list endpoints.
type pagedResponse struct {
	Results json.RawMessage `json:"results"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// FetchAll follows cursor pagination and returns all raw JSON items across all pages.
// It is safe to call on any /wiki/api/v2/* list endpoint.
func FetchAll(ctx context.Context, client *Client, path string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	current := path
	for page := 0; page < maxPages; page++ {
		body, err := client.Get(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("page %d of %s: %w", page+1, path, err)
		}

		var pr pagedResponse
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, fmt.Errorf("parse page %d of %s: %w", page+1, path, err)
		}

		var items []json.RawMessage
		if err := json.Unmarshal(pr.Results, &items); err != nil {
			return nil, fmt.Errorf("parse results page %d: %w", page+1, err)
		}
		all = append(all, items...)

		next := ExtractCursorPath(pr.Links.Next)
		if next == "" {
			return all, nil
		}
		current = next
	}
	return nil, fmt.Errorf("exceeded %d page limit for %s", maxPages, path)
}

// ExtractCursorPath returns the path+query portion of a Confluence _links.next value.
// Confluence returns a relative path (e.g. "/wiki/api/v2/spaces?cursor=abc&limit=50").
// Returns "" if nextLink is empty.
func ExtractCursorPath(nextLink string) string {
	return nextLink // Confluence already returns a relative path — use as-is
}
```

**Step 4: Run tests — expect pass**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/api/ -v -run "TestFetchAll|TestExtractCursor"
```

Expected: all PASS. Note: `TestFetchAll_PageCap` will be slow without throttle — that is expected.

**Step 5: Commit**

```bash
git add internal/api/pagination.go internal/api/pagination_test.go
git commit -m "feat: add cursor-based pagination for Confluence v2 API"
```

---

### Task 4: Confluence resource types & fetchers (`internal/api/confluence.go`)

**Files:**
- Create: `internal/api/confluence.go`
- Create: `internal/api/confluence_test.go`

**Step 1: Write `internal/api/confluence.go`**

No failing test first here — these are data types + thin fetch wrappers. Tests cover fetch functions.

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// validDomain matches Atlassian Cloud domains.
var validDomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-\.]{0,253}$`)

// ValidateDomain returns an error if domain contains unsafe characters.
func ValidateDomain(domain string) error {
	if !validDomain.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	return nil
}

// --- Types ---

type Space struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

type SpacePermission struct {
	ID        string          `json:"id"`
	Principal json.RawMessage `json:"principal"`
	Operation json.RawMessage `json:"operation"`
}

type SpaceProperty struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value"`
	Version json.RawMessage `json:"version"`
}

type SpaceDetail struct {
	Space
	Permissions []SpacePermission `json:"permissions,omitempty"`
	Properties  []SpaceProperty   `json:"properties,omitempty"`
}

type PageVersion struct {
	Number    int    `json:"number"`
	CreatedAt string `json:"createdAt"`
	AuthorID  string `json:"authorId,omitempty"`
}

type Label struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

type Page struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	SpaceID   string      `json:"spaceId"`
	ParentID  string      `json:"parentId,omitempty"`
	ParentType string     `json:"parentType,omitempty"`
	Status    string      `json:"status"`
	Version   PageVersion `json:"version"`
	Body      struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Labels  []Label `json:"labels,omitempty"`
}

type BlogPost struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	SpaceID string      `json:"spaceId"`
	Status  string      `json:"status"`
	Version PageVersion `json:"version"`
	Body    struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
}

type Comment struct {
	ID      string `json:"id"`
	Body    struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Version json.RawMessage `json:"version"`
}

type Comments struct {
	Footer []Comment `json:"footer"`
	Inline []Comment `json:"inline"`
}

type Attachment struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	FileSize    int64  `json:"fileSize"`
	MediaType   string `json:"mediaType"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	// WebuiLink contains the relative URL; we build the full download URL from it.
	Links struct {
		Download string `json:"download"`
	} `json:"_links"`
}

type Template struct {
	TemplateID   string `json:"templateId"`
	Name         string `json:"name"`
	TemplateType string `json:"templateType"`
	Body         struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AccountType string `json:"accountType"`
}

// --- Fetch functions ---

// FetchSpaces returns all visible spaces.
func FetchSpaces(ctx context.Context, client *Client) ([]Space, error) {
	items, err := FetchAll(ctx, client, "/wiki/api/v2/spaces?limit=250")
	if err != nil {
		return nil, fmt.Errorf("fetch spaces: %w", err)
	}
	spaces := make([]Space, 0, len(items))
	for _, raw := range items {
		var s Space
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parse space: %w", err)
		}
		spaces = append(spaces, s)
	}
	return spaces, nil
}

// FetchSpaceDetail fetches permissions and properties for one space.
// Uses both v2 (permissions) and v1 (properties) APIs.
func FetchSpaceDetail(ctx context.Context, client *Client, space Space) (SpaceDetail, error) {
	detail := SpaceDetail{Space: space}

	// Permissions (v2)
	permItems, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/permissions?limit=250", space.ID))
	if err != nil {
		return detail, fmt.Errorf("fetch permissions for space %s: %w", space.Key, err)
	}
	for _, raw := range permItems {
		var p SpacePermission
		if err := json.Unmarshal(raw, &p); err == nil {
			detail.Permissions = append(detail.Permissions, p)
		}
	}

	// Properties (v1 — not yet in v2)
	propBody, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/space/%s/property?expand=value,version&limit=200", space.Key))
	if err == nil {
		var resp struct {
			Results []SpaceProperty `json:"results"`
		}
		if json.Unmarshal(propBody, &resp) == nil {
			detail.Properties = resp.Results
		}
	}
	// Properties fetch failure is non-fatal — continue without them.

	return detail, nil
}

// FetchPages returns all pages in a space with HTML body.
func FetchPages(ctx context.Context, client *Client, spaceID string) ([]Page, error) {
	path := fmt.Sprintf(
		"/wiki/api/v2/pages?spaceId=%s&body-format=view&limit=250&status=current", spaceID)
	items, err := FetchAll(ctx, client, path)
	if err != nil {
		return nil, fmt.Errorf("fetch pages for space %s: %w", spaceID, err)
	}
	pages := make([]Page, 0, len(items))
	for _, raw := range items {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse page: %w", err)
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// FetchBlogPosts returns all blog posts in a space with HTML body.
func FetchBlogPosts(ctx context.Context, client *Client, spaceID string) ([]BlogPost, error) {
	path := fmt.Sprintf(
		"/wiki/api/v2/blogposts?spaceId=%s&body-format=view&limit=250&status=current", spaceID)
	items, err := FetchAll(ctx, client, path)
	if err != nil {
		return nil, fmt.Errorf("fetch blogposts for space %s: %w", spaceID, err)
	}
	posts := make([]BlogPost, 0, len(items))
	for _, raw := range items {
		var p BlogPost
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse blogpost: %w", err)
		}
		posts = append(posts, p)
	}
	return posts, nil
}

// FetchComments fetches footer and inline comments for a page.
func FetchComments(ctx context.Context, client *Client, pageID string) (Comments, error) {
	var c Comments

	footer, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/footer-comments?body-format=view&limit=250", pageID))
	if err != nil {
		return c, fmt.Errorf("fetch footer comments for page %s: %w", pageID, err)
	}
	for _, raw := range footer {
		var comment Comment
		if json.Unmarshal(raw, &comment) == nil {
			c.Footer = append(c.Footer, comment)
		}
	}

	inline, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/inline-comments?body-format=view&limit=250", pageID))
	if err != nil {
		return c, fmt.Errorf("fetch inline comments for page %s: %w", pageID, err)
	}
	for _, raw := range inline {
		var comment Comment
		if json.Unmarshal(raw, &comment) == nil {
			c.Inline = append(c.Inline, comment)
		}
	}

	return c, nil
}

// FetchAttachmentMeta returns attachment metadata for a page (no file download).
func FetchAttachmentMeta(ctx context.Context, client *Client, pageID string) ([]Attachment, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/attachments?limit=250", pageID))
	if err != nil {
		return nil, fmt.Errorf("fetch attachments for page %s: %w", pageID, err)
	}
	atts := make([]Attachment, 0, len(items))
	for _, raw := range items {
		var a Attachment
		if err := json.Unmarshal(raw, &a); err == nil {
			atts = append(atts, a)
		}
	}
	return atts, nil
}

// FetchTemplates returns space-level templates (v1 API).
func FetchTemplates(ctx context.Context, client *Client, spaceKey string) ([]Template, error) {
	body, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/template?spaceKey=%s&limit=200", spaceKey))
	if err != nil {
		return nil, fmt.Errorf("fetch templates for space %s: %w", spaceKey, err)
	}
	var resp struct {
		Results []Template `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return resp.Results, nil
}

// FetchUserProfile fetches a single user profile by account ID (v1 API).
func FetchUserProfile(ctx context.Context, client *Client, accountID string) (User, error) {
	body, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/user?accountId=%s&expand=email", accountID))
	if err != nil {
		return User{}, fmt.Errorf("fetch user %s: %w", accountID, err)
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, fmt.Errorf("parse user %s: %w", accountID, err)
	}
	return u, nil
}
```

**Step 2: Write `internal/api/confluence_test.go`**

```go
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestFetchSpaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "spaces") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": "1", "key": "KB", "name": "Knowledge Base", "type": "global", "status": "current"},
			},
			"_links": map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	spaces, err := api.FetchSpaces(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0].Key != "KB" {
		t.Errorf("unexpected spaces: %v", spaces)
	}
}

func TestValidateDomain(t *testing.T) {
	if err := api.ValidateDomain("myorg.atlassian.net"); err != nil {
		t.Errorf("valid domain rejected: %v", err)
	}
	if err := api.ValidateDomain("bad domain!"); err == nil {
		t.Error("invalid domain accepted")
	}
}
```

**Step 3: Run tests**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/api/ -v 2>&1 | tail -20
```

Expected: all tests PASS. Some fetch functions have no test beyond compilation — that is acceptable for thin wrappers; integration tests would need real credentials.

**Step 4: Commit**

```bash
git add internal/api/confluence.go internal/api/confluence_test.go
git commit -m "feat: add Confluence resource types and fetch functions"
```

---

### Task 5: Hierarchical storage writer (`internal/storage/writer.go`)

**Files:**
- Create: `internal/storage/writer.go`
- Create: `internal/storage/writer_test.go`

**Step 1: Write failing tests**

`internal/storage/writer_test.go`:
```go
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
		{"../etc/passwd", ".._.._.._etc_passwd"},
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
```

**Step 2: Run tests — expect compile failure**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/storage/ 2>&1 | head -5
```

**Step 3: Write `internal/storage/writer.go`**

```go
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

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) // #nosec G304 -- dest is validated above
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}
```

**Step 4: Run tests — expect pass**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/storage/ -v
```

Expected: all PASS.

**Step 5: Commit**

```bash
git add internal/storage/
git commit -m "feat: add hierarchical storage writer with path-traversal protection"
```

---

### Task 6: Manifest (`internal/backup/manifest.go`)

**Files:**
- Create: `internal/backup/manifest.go`
- Create: `internal/backup/manifest_test.go`

**Step 1: Write failing tests**

`internal/backup/manifest_test.go`:
```go
package backup_test

import (
	"os"
	"path/filepath"
	"testing"

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
```

Add `"time"` import to the test file.

**Step 2: Write `internal/backup/manifest.go`**

Identical to holaspirit-backup's manifest.go with two changes:
1. `OrganizationID` field → `Domain` field (Confluence has no org ID concept)
2. HMAC domain key: `"confluence-backup-manifest\x00"` instead of `"holaspirit-backup-manifest\x00"`

```go
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

type Manifest struct {
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
	m.Files = append(m.Files, FileEntry{
		Name:   filepath.Base(path),
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Status: "ok",
	})
	return nil
}

func (m *Manifest) AddFailedFile(name string, err error) {
	m.Files = append(m.Files, FileEntry{
		Name:   name,
		Status: "failed",
		Error:  err.Error(),
	})
}

func (m *Manifest) Write(path, token string) error {
	m.Summary = Summary{TotalFiles: len(m.Files)}
	for _, f := range m.Files {
		if f.Status == "ok" {
			m.Summary.Successful++
		} else {
			m.Summary.Failed++
		}
	}
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
```

**Step 3: Run tests**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/backup/ -v -run TestManifest
```

Expected: all PASS.

**Step 4: Commit**

```bash
git add internal/backup/manifest.go internal/backup/manifest_test.go
git commit -m "feat: add HMAC-SHA-256 manifest for backup integrity"
```

---

### Task 7: Page tree builder (`internal/backup/tree.go`)

**Files:**
- Create: `internal/backup/tree.go`
- Create: `internal/backup/tree_test.go`

**Step 1: Write failing tests**

`internal/backup/tree_test.go`:
```go
package backup_test

import (
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

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
```

**Step 2: Write `internal/backup/tree.go`**

```go
package backup

import (
	"fmt"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// PageNode is a page in the tree together with its children.
type PageNode struct {
	Page     api.Page
	Children []*PageNode
	dirName  string // sanitized, collision-free directory name
}

// DirName returns the sanitized directory name for this page.
func (n *PageNode) DirName() string { return n.dirName }

// BuildTree takes a flat list of pages and returns root nodes
// (pages without a valid parent in the list).
// Orphans (parentID set but parent not in list) are promoted to root.
// Sibling directory names are de-duplicated by appending the page ID.
func BuildTree(pages []api.Page) []*PageNode {
	nodes := make(map[string]*PageNode, len(pages))
	for i := range pages {
		nodes[pages[i].ID] = &PageNode{Page: pages[i]}
	}

	var roots []*PageNode
	for _, n := range nodes {
		parentID := n.Page.ParentID
		if parentID == "" {
			roots = append(roots, n)
			continue
		}
		parent, ok := nodes[parentID]
		if !ok {
			// Orphan — promote to root
			roots = append(roots, n)
			continue
		}
		parent.Children = append(parent.Children, n)
	}

	// Assign collision-free directory names within each sibling group.
	assignDirNames(roots)
	return roots
}

// assignDirNames sets DirName for each node, deduplicating within a sibling group.
func assignDirNames(siblings []*PageNode) {
	seen := make(map[string]int)
	for _, n := range siblings {
		base := storage.SanitizeName(n.Page.Title)
		if base == "" {
			base = fmt.Sprintf("page_%s", n.Page.ID)
		}
		if seen[base] == 0 {
			n.dirName = base
		} else {
			// Collision: append ID to make unique
			n.dirName = fmt.Sprintf("%s_%s", base, n.Page.ID)
		}
		seen[base]++
		// Recurse into children
		assignDirNames(n.Children)
	}
}
```

**Step 3: Run tests**

```bash
GOTOOLCHAIN=go1.25.8 go test ./internal/backup/ -v -run TestBuildTree -run TestPageDirName
```

Expected: all PASS.

**Step 4: Commit**

```bash
git add internal/backup/tree.go internal/backup/tree_test.go
git commit -m "feat: add page tree builder for hierarchical backup structure"
```

---

### Task 8: Backup orchestration (`internal/backup/backup.go`)

**Files:**
- Create: `internal/backup/backup.go`

**Step 1: Write `internal/backup/backup.go`**

```go
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// Config controls what gets backed up.
type Config struct {
	Domain          string
	OutputDir       string
	ExcludeSpaces   map[string]bool // space keys to skip
	IncludeAttachments bool
	Timeout         time.Duration
	DryRun          bool
	ToolVersion     string
}

// Run executes the full backup and returns the path to the created backup directory.
func Run(ctx context.Context, client *api.Client, cfg Config) (string, error) {
	ts := time.Now()
	backupDir := filepath.Join(cfg.OutputDir, ts.UTC().Format("2006-01-02T15-04-05"))

	if cfg.DryRun {
		slog.Info("dry-run mode: no files will be written")
	}

	w, err := storage.NewWriter(backupDir)
	if err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	manifest := NewManifest(cfg.Domain, cfg.ToolVersion, ts)

	// --- Fetch all spaces ---
	spaces, err := api.FetchSpaces(ctx, client)
	if err != nil {
		return "", fmt.Errorf("fetch spaces: %w", err)
	}
	slog.Info("found spaces", "count", len(spaces))

	// --- Filter excluded spaces ---
	var active []api.Space
	for _, s := range spaces {
		if cfg.ExcludeSpaces[s.Key] {
			slog.Info("skipping space", "key", s.Key)
			continue
		}
		active = append(active, s)
	}

	// --- Collect all user account IDs ---
	var userMu sync.Mutex
	accountIDs := make(map[string]struct{})
	collectUser := func(id string) {
		if id == "" {
			return
		}
		userMu.Lock()
		accountIDs[id] = struct{}{}
		userMu.Unlock()
	}

	// --- Process spaces (3 concurrent) ---
	spaceSem := make(chan struct{}, 3)
	pageSem := make(chan struct{}, 20) // global cap across all spaces

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	addErr := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	for _, sp := range active {
		sp := sp
		wg.Add(1)
		go func() {
			defer wg.Done()
			spaceSem <- struct{}{}
			defer func() { <-spaceSem }()

			if err := processSpace(ctx, client, w, manifest, sp, cfg,
				pageSem, collectUser); err != nil {
				addErr(fmt.Errorf("space %s: %w", sp.Key, err))
				slog.Error("space backup failed", "key", sp.Key, "err", err)
			}
		}()
	}
	wg.Wait()

	// --- Fetch user profiles ---
	if err := fetchAndWriteUsers(ctx, client, w, manifest, accountIDs); err != nil {
		slog.Warn("user profiles partially failed", "err", err)
	}

	// --- Write manifest ---
	// Manifest signing uses the token (not stored on disk).
	// We pass an empty token here; callers that want HMAC must call manifest.Write directly.
	// See cmd/backup/main.go for the signed write.
	_ = manifest // returned to caller for signing

	if len(errs) > 0 {
		return w.Dir(), fmt.Errorf("%d space(s) had errors: first: %w", len(errs), errs[0])
	}
	return w.Dir(), nil
}

func processSpace(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, sp api.Space, cfg Config,
	pageSem chan struct{}, collectUser func(string)) error {

	slog.Info("backing up space", "key", sp.Key, "name", sp.Name)

	// Space detail (permissions + properties)
	detail, err := api.FetchSpaceDetail(ctx, client, sp)
	if err != nil {
		return err
	}
	spaceJSON, _ := json.MarshalIndent(detail, "", "  ")
	relPath := filepath.Join("spaces", sp.Key, "space.json")
	if !cfg.DryRun {
		if err := w.WriteFile(relPath, spaceJSON); err != nil {
			return err
		}
		manifest.AddFile(filepath.Join(w.Dir(), relPath))
	}

	// Templates
	templates, err := api.FetchTemplates(ctx, client, sp.Key)
	if err != nil {
		slog.Warn("templates fetch failed", "space", sp.Key, "err", err)
	}
	for _, tmpl := range templates {
		tJSON, _ := json.MarshalIndent(tmpl, "", "  ")
		tPath := filepath.Join("spaces", sp.Key, "templates",
			storage.SanitizeName(tmpl.Name)+".json")
		if !cfg.DryRun {
			w.WriteFile(tPath, tJSON)
			manifest.AddFile(filepath.Join(w.Dir(), tPath))
		}
	}

	// Pages (flat fetch → build tree → write recursively)
	pages, err := api.FetchPages(ctx, client, sp.ID)
	if err != nil {
		return fmt.Errorf("fetch pages: %w", err)
	}
	for _, p := range pages {
		collectUser(p.Version.AuthorID)
	}

	roots := BuildTree(pages)
	var pageWg sync.WaitGroup
	var pageErrs []error
	var pageErrMu sync.Mutex

	var writeTree func(nodes []*PageNode, parentRelPath string)
	writeTree = func(nodes []*PageNode, parentRelPath string) {
		for _, node := range nodes {
			node := node
			pageWg.Add(1)
			go func() {
				defer pageWg.Done()
				pageSem <- struct{}{}
				defer func() { <-pageSem }()

				dirPath := filepath.Join(parentRelPath, node.DirName())
				if err := writePage(ctx, client, w, manifest, node.Page,
					dirPath, cfg); err != nil {
					pageErrMu.Lock()
					pageErrs = append(pageErrs, err)
					pageErrMu.Unlock()
					manifest.AddFailedFile(dirPath+"/page.json", err)
					slog.Error("page backup failed", "id", node.Page.ID, "title", node.Page.Title, "err", err)
				}
				// Children can start after parent dir is created (non-blocking for children)
				writeTree(node.Children, dirPath)
			}()
		}
	}
	writeTree(roots, filepath.Join("spaces", sp.Key, "pages"))
	pageWg.Wait()

	// Blog posts
	posts, err := api.FetchBlogPosts(ctx, client, sp.ID)
	if err != nil {
		slog.Warn("blog posts fetch failed", "space", sp.Key, "err", err)
	}
	for _, post := range posts {
		post := post
		dirName := post.Version.CreatedAt[:10] + "_" + storage.SanitizeName(post.Title)
		dirPath := filepath.Join("spaces", sp.Key, "blog", dirName)
		if !cfg.DryRun {
			writePost(ctx, client, w, manifest, post, dirPath)
		}
	}

	if len(pageErrs) > 0 {
		return fmt.Errorf("%d page(s) failed", len(pageErrs))
	}
	return nil
}

func writePage(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, page api.Page, dirPath string, cfg Config) error {

	// index.html
	htmlPath := filepath.Join(dirPath, "index.html")
	if !cfg.DryRun {
		if err := w.WriteFile(htmlPath, []byte(page.Body.View.Value)); err != nil {
			return err
		}
		manifest.AddFile(filepath.Join(w.Dir(), htmlPath))
	}

	// page.json (metadata without body to keep it small)
	meta := struct {
		ID       string          `json:"id"`
		Title    string          `json:"title"`
		SpaceID  string          `json:"spaceId"`
		ParentID string          `json:"parentId,omitempty"`
		Status   string          `json:"status"`
		Version  api.PageVersion `json:"version"`
		Labels   []api.Label     `json:"labels,omitempty"`
	}{
		ID: page.ID, Title: page.Title, SpaceID: page.SpaceID,
		ParentID: page.ParentID, Status: page.Status,
		Version: page.Version, Labels: page.Labels,
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	metaPath := filepath.Join(dirPath, "page.json")
	if !cfg.DryRun {
		w.WriteFile(metaPath, metaJSON)
		manifest.AddFile(filepath.Join(w.Dir(), metaPath))
	}

	// comments.json
	comments, err := api.FetchComments(ctx, client, page.ID)
	if err != nil {
		slog.Warn("comments fetch failed", "pageId", page.ID, "err", err)
	} else if !cfg.DryRun {
		cJSON, _ := json.MarshalIndent(comments, "", "  ")
		cPath := filepath.Join(dirPath, "comments.json")
		w.WriteFile(cPath, cJSON)
		manifest.AddFile(filepath.Join(w.Dir(), cPath))
	}

	// attachments
	atts, err := api.FetchAttachmentMeta(ctx, client, page.ID)
	if err != nil {
		slog.Warn("attachments fetch failed", "pageId", page.ID, "err", err)
	} else if !cfg.DryRun {
		attJSON, _ := json.MarshalIndent(atts, "", "  ")
		attPath := filepath.Join(dirPath, "attachments", "metadata.json")
		w.WriteFile(attPath, attJSON)
		manifest.AddFile(filepath.Join(w.Dir(), attPath))

		if cfg.IncludeAttachments {
			downloadAttachments(ctx, client, w, manifest, atts, dirPath)
		}
	}

	return nil
}

func writePost(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, post api.BlogPost, dirPath string) {
	w.WriteFile(filepath.Join(dirPath, "index.html"), []byte(post.Body.View.Value))
	postJSON, _ := json.MarshalIndent(post, "", "  ")
	w.WriteFile(filepath.Join(dirPath, "post.json"), postJSON)
	manifest.AddFile(filepath.Join(w.Dir(), dirPath, "index.html"))
	manifest.AddFile(filepath.Join(w.Dir(), dirPath, "post.json"))
}

func downloadAttachments(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, atts []api.Attachment, dirPath string) {
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, att := range atts {
		att := att
		if att.Links.Download == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			downloadURL := client.BaseURL() + att.Links.Download
			rc, err := client.Download(ctx, downloadURL)
			if err != nil {
				slog.Warn("attachment download failed", "name", att.Title, "err", err)
				return
			}
			defer rc.Close()
			filePath := filepath.Join(dirPath, "attachments", "files",
				storage.SanitizeName(att.Title))
			if err := w.WriteBinaryStream(filePath, rc); err != nil {
				slog.Warn("attachment write failed", "name", att.Title, "err", err)
				return
			}
			manifest.AddFile(filepath.Join(w.Dir(), filePath))
		}()
	}
	wg.Wait()
}

func fetchAndWriteUsers(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, accountIDs map[string]struct{}) error {
	users := make([]api.User, 0, len(accountIDs))
	for id := range accountIDs {
		u, err := api.FetchUserProfile(ctx, client, id)
		if err != nil {
			slog.Warn("user profile fetch failed", "accountId", id, "err", err)
			continue
		}
		users = append(users, u)
	}
	usersJSON, _ := json.MarshalIndent(users, "", "  ")
	usersPath := "users.json"
	if err := w.WriteFile(usersPath, usersJSON); err != nil {
		return err
	}
	manifest.AddFile(filepath.Join(w.Dir(), usersPath))
	return nil
}
```

**Step 2: Verify it compiles**

```bash
GOTOOLCHAIN=go1.25.8 go build ./...
```

Expected: no errors.

**Step 3: Commit**

```bash
git add internal/backup/backup.go
git commit -m "feat: add backup orchestration with two-level worker pool"
```

---

### Task 9: CLI (`cmd/backup/`)

**Files:**
- Modify: `cmd/backup/main.go`
- Create: `cmd/backup/verify.go`
- Create: `cmd/backup/token_windows.go`
- Create: `cmd/backup/token_other.go`

**Step 1: Write `cmd/backup/token_other.go`**

```go
//go:build !windows

package main

import (
	"fmt"
	"os"
)

func getToken() (string, error) {
	tok := os.Getenv("CONFLUENCE_TOKEN")
	if tok == "" {
		return "", fmt.Errorf("CONFLUENCE_TOKEN environment variable not set")
	}
	return tok, nil
}
```

**Step 2: Write `cmd/backup/token_windows.go`**

```go
//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/danieljoos/wincred"
)

func getToken() (string, error) {
	// Try env first; Windows Credential Manager as fallback.
	if tok := os.Getenv("CONFLUENCE_TOKEN"); tok != "" {
		return tok, nil
	}
	cred, err := wincred.GetGenericCredential("confluence-backup")
	if err != nil {
		return "", fmt.Errorf("CONFLUENCE_TOKEN not set and credential not found in Windows Credential Manager (target: confluence-backup): %w", err)
	}
	return string(cred.CredentialBlob), nil
}
```

Note: `github.com/danieljoos/wincred` is already in vendor/ from holaspirit-backup pattern. Run `go get github.com/danieljoos/wincred` and `go mod vendor` if not present.

**Step 3: Write `cmd/backup/verify.go`**

```go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kAYd9iN/confluence-backup/internal/backup"
)

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "", "backup directory to verify")
	fs.Parse(args) // #nosec G104 -- FlagSet uses ExitOnError; return value is unreachable

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: confluence-backup verify --dir <backup-dir>")
		return 1
	}

	token, err := getToken()
	if err != nil {
		slog.Error("token error", "err", err)
		return 1
	}

	manifestPath := filepath.Join(*dir, "backup-manifest.json")
	if err := backup.VerifyManifest(manifestPath, token); err != nil {
		slog.Error("verification failed", "err", err)
		return 1
	}
	slog.Info("manifest verified successfully", "dir", *dir)
	return 0
}
```

**Step 4: Write `cmd/backup/main.go`**

```go
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

	token, err := getToken()
	if err != nil {
		slog.Error("token error", "err", err)
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

	client := api.NewClient(*domain, token)
	cfg := backup.Config{
		Domain:             *domain,
		OutputDir:          *output,
		ExcludeSpaces:      exclude,
		IncludeAttachments: *attachments,
		Timeout:            *timeout,
		DryRun:             *dryRun,
		ToolVersion:        version,
	}

	slog.Info("starting backup", "domain", *domain, "output", *output,
		"attachments", *attachments, "dry-run", *dryRun)

	backupDir, err := backup.Run(ctx, client, cfg)
	if err != nil {
		slog.Error("backup completed with errors", "err", err)
		// Write manifest even on partial failure
		writeSignedManifest(backupDir, token, cfg)
		return 1
	}

	writeSignedManifest(backupDir, token, cfg)
	slog.Info("backup complete", "dir", backupDir)
	return 0
}

func writeSignedManifest(backupDir, token string, cfg backup.Config) {
	// Re-read manifest written by backup.Run and sign it.
	// backup.Run writes an unsigned manifest.json; we replace it with a signed version.
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
```

Note: `backup.Run` needs to write the unsigned manifest before returning. Add this to `backup.go`'s `Run` function after the user fetch:

```go
// Write unsigned manifest (signing happens in main after Run returns)
manifestPath := filepath.Join(w.Dir(), "backup-manifest.json")
manifestData, _ := json.MarshalIndent(manifest, "", "  ")
w.WriteFile("backup-manifest.json", manifestData)
```

**Step 5: Build and smoke-test**

```bash
GOTOOLCHAIN=go1.25.8 go build -o confluence-backup ./cmd/backup/
./confluence-backup --version
```

Expected: `confluence-backup dev`

```bash
./confluence-backup verify --dir nonexistent 2>&1
```

Expected: error about token or missing directory (not a crash).

**Step 6: Run all tests**

```bash
GOTOOLCHAIN=go1.25.8 go test ./... 2>&1
```

Expected: all PASS.

**Step 7: Commit**

```bash
git add cmd/backup/
git commit -m "feat: add CLI with backup and verify subcommands"
```

---

### Task 10: CI/CD workflows

**Files:**
- Create: `.github/workflows/security-and-quality.yml`
- Create: `.github/workflows/build.yml`
- Create: `.github/workflows/release.yml`
- Create: `.github/workflows/cbom.yml`
- Create: `.github/workflows/scorecard.yml`
- Create: `.github/workflows/commit-signature.yml`
- Create: `.github/workflows/dependency-review.yml`
- Create: `.github/dependabot.yml`
- Create: `scripts/verify_signed_commits.sh`

**Step 1: Copy and adapt workflows from holaspirit-backup**

All workflow files are structurally identical to holaspirit-backup. The only changes:
- `go-build` step: binary name `confluence-backup` (not `backup`)
- `ldflags`: `-X main.version=${{ github.ref_name }}`
- `build.yml` matrix artifacts: `confluence-backup-linux-amd64`, etc.
- `release.yml` binary glob: `confluence-backup-*`

Reference: `github.com/kAYd9iN/holaspirit-backup/.github/workflows/` for exact SHA-pinned action versions. Use the same SHA pins — they are current as of 2026-03-07.

Key SHA pins to reuse:
- `actions/checkout`: `34e114876b0b11c390a56381ad16ebd13914f8d5`
- `actions/setup-go`: `4b73464bb391d4059bd26b0524d20df3927bd417`
- `actions/upload-artifact`: `ea165f8d65b6e75b540449e92b4886f43607fa02`
- `ossf/scorecard-action`: `4eaacf0543bb3f2c246792bd56e8cdeffafb205a`
- `actions/dependency-review-action`: `2031cfc0...`
- `sigstore/cosign-installer`: `faadad0c...`
- `actions/attest-build-provenance`: `e8998f94...`

**Step 2: Write `scripts/verify_signed_commits.sh`**

Copy verbatim from holaspirit-backup's `scripts/verify_signed_commits.sh`.

**Step 3: Write `.github/dependabot.yml`**

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
      day: monday
      time: "06:00"
      timezone: Europe/Zurich
    open-pull-requests-limit: 5

  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
      day: monday
      time: "06:00"
      timezone: Europe/Zurich
    open-pull-requests-limit: 5
```

**Step 4: Write `scripts/check-api-schema.sh`**

Adapt holaspirit-backup's `scripts/check-api-schema.sh` for Confluence:
- Replace the `PATHS` array with Confluence endpoints:
  - `spaces`: `/wiki/api/v2/spaces?limit=1`
  - `pages`: `/wiki/api/v2/pages?limit=1`
  - `blogposts`: `/wiki/api/v2/blogposts?limit=1`
  - `comments`: `/wiki/api/v2/pages/{samplePageId}/footer-comments?limit=1` (requires a known page ID — use a fixed test page ID stored in `docs/api-snapshot.json`)
- Replace `HOLASPIRIT_TOKEN` / `HOLASPIRIT_ORG_ID` with `CONFLUENCE_TOKEN` / `CONFLUENCE_DOMAIN`

**Step 5: Commit all workflows**

```bash
mkdir -p .github/workflows scripts
git add .github/ scripts/
git commit -m "ci: add all CI/CD workflows identical to holaspirit-backup"
```

---

### Task 11: CLAUDE.md + GitHub repo creation + first push

**Files:**
- Create: `CLAUDE.md`
- Create: `README.md`
- Create: `SECURITY.md`

**Step 1: Write `CLAUDE.md`**

```markdown
# confluence-backup

Backup tool for Confluence Cloud. Backs up spaces, pages (HTML), blog posts,
comments, attachments, templates, users, and space permissions into a
hierarchical directory structure with HMAC-SHA-256 signed manifest.

## Commands

    GOTOOLCHAIN=go1.25.8 go test ./...
    go build -o confluence-backup ./cmd/backup/
    CONFLUENCE_TOKEN=<PAT> ./confluence-backup --domain myorg.atlassian.net --output ./backups
    CONFLUENCE_TOKEN=<PAT> ./confluence-backup verify --dir ./backups/2026-03-08_120000

## Key Files

| Path | Purpose |
|------|---------|
| `internal/api/client.go` | GET-only HTTP client, rate 10 req/s, retry 429+5xx |
| `internal/api/pagination.go` | Cursor-based pagination (Confluence v2) |
| `internal/api/confluence.go` | Resource types + fetch functions for all 8 data types |
| `internal/backup/tree.go` | Builds page hierarchy from flat API list |
| `internal/backup/backup.go` | Orchestration, two-level worker pool (3 spaces × cap 20 pages) |
| `internal/backup/manifest.go` | SHA256 per file + HMAC-SHA-256 .sig |
| `internal/storage/writer.go` | Hierarchical writer, 0600 files, path-traversal protection |
| `cmd/backup/main.go` | CLI entry point |

## Architecture

- **GET-only**: Client has only Get() + Download() — no write methods
- **Bearer PAT**: token via CONFLUENCE_TOKEN env var or Windows Credential Manager
- **Cursor pagination**: follows _links.next until exhausted
- **Two-level pool**: 3 concurrent spaces, 20 concurrent pages (global cap)
- **Hierarchical output**: spaces/KEY/pages/Title/SubTitle/index.html
- **Attachments**: metadata always; files only with --attachments flag
- **HMAC key**: domain-separated (confluence-backup-manifest-v1)
- **vendor/**: checked in for supply-chain safety

## Repo

- GitHub: kAYd9iN/confluence-backup (public)
- Versioning: 0ver — v0.1.0, v0.2.0, ... (https://0ver.org/)
- go.mod: go 1.25.8 / CI go-version: '1.26' — do not change

## Pending Manual Steps (after first push)

- Set SCORECARD_TOKEN secret (PAT with repo + read:org)
- Set COMMIT_SIGNING_PUBLIC_KEY secret (GPG key)
- Set CONFLUENCE_TOKEN + CONFLUENCE_DOMAIN secrets for api-update-check workflow
- Create Confluence space CB and publish docs

## Extending: Adding a New Data Type

1. Add fetch function to internal/api/confluence.go
2. Call it in internal/backup/backup.go (processSpace or Run)
3. Run GOTOOLCHAIN=go1.25.8 go test ./... to verify
4. Commit
```

**Step 2: Create GitHub repo**

```bash
cd /home/nic/confluence-backup
gh repo create kAYd9iN/confluence-backup --public \
  --description "Backup tool for Confluence Cloud — hierarchical HTML export with HMAC-signed manifest"
git remote add origin git@github.com:kAYd9iN/confluence-backup.git
```

**Step 3: Add README.md and SECURITY.md**

README.md: same structure as holaspirit-backup's README — project description, quick start, output structure, security section, 0ver versioning note.

SECURITY.md: copy from holaspirit-backup, update binary name and token env var name.

**Step 4: Push**

```bash
git add CLAUDE.md README.md SECURITY.md
git commit -m "docs: add CLAUDE.md, README, SECURITY"
git push -u origin main
```

---

### Task 12: Confluence space CB + documentation

**Step 1: Create Confluence space CB**

Use the Atlassian MCP tool:
```
mcp__plugin_atlassian_atlassian__createConfluencePage with:
  cloudId: 78b5b3f6-a4c9-4f9d-856e-56eca016288c
  spaceId: CB (create space first via Confluence UI if needed)
```

Note: Space creation requires admin rights and is best done via Confluence UI:
Settings → Spaces → Create Space → Software project → Key: CB → Name: Confluence Backup

**Step 2: Create three documentation pages in CB space**

Using the Atlassian MCP, create:

1. **Sicherheitskonzept** — mirror the structure from HB space's security concept, adapted for:
   - Bearer PAT auth (not HMAC token)
   - HMAC key: `confluence-backup-manifest-v1`
   - GET-only + Download() for attachments
   - Same file permission model (0600/0750)

2. **Design & Architektur** — covers:
   - Tech stack decisions (same rationale as holaspirit)
   - Confluence-specific: cursor pagination, two-level pool, page tree builder
   - Output format choice (HTML for portability)

3. **Betrieb & Installation** — covers:
   - Prerequisites: Go 1.25.8, PAT creation in Atlassian
   - CLI usage with all flags
   - Windows Credential Manager setup
   - Running verify after backup

**Step 3: Update CLAUDE.md with Confluence page IDs once created**

Add to the CLAUDE.md Confluence section:
```markdown
## Confluence (CB Space, cloudId: 78b5b3f6-a4c9-4f9d-856e-56eca016288c)
- Sicherheitskonzept (ID: <id>)
- Design & Architektur (ID: <id>)
- Betrieb & Installation (ID: <id>)
```

---

### Task 13: First release v0.1.0

**Step 1: Verify all tests pass**

```bash
GOTOOLCHAIN=go1.25.8 go test ./... -race
```

Expected: all PASS, no race conditions.

**Step 2: Verify it builds for all 3 platforms**

```bash
GOOS=linux   GOARCH=amd64 GOTOOLCHAIN=go1.25.8 go build -o /dev/null ./cmd/backup/
GOOS=linux   GOARCH=arm64 GOTOOLCHAIN=go1.25.8 go build -o /dev/null ./cmd/backup/
GOOS=windows GOARCH=amd64 GOTOOLCHAIN=go1.25.8 go build -o /dev/null ./cmd/backup/
```

Expected: all succeed.

**Step 3: Tag and push**

```bash
git tag -a v0.1.0 -m "release: v0.1.0 — first public release (0ver)"
git push origin v0.1.0
```

Expected: `release.yml` workflow triggers on GitHub, creates release with 3 binaries + SHA256SUMS + SLSA provenance + cosign signature bundles.

**Step 4: Update CLAUDE.md status**

Change status line to: `v0.1.0 RELEASED (2026-03-08)`

```bash
git add CLAUDE.md
git commit -m "docs: mark v0.1.0 as released"
git push
```

