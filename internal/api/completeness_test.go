package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func newTestClient(srv *httptest.Server) *api.Client {
	c := api.NewClient(srv.URL, "u@example.com", "tok")
	c.MaxRetries = 0
	return c
}

// Archived pages are real content: both statuses must be requested.
func TestFetchPages_IncludesArchived(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "_links": map[string]any{}})
	}))
	defer srv.Close()

	api.FetchPages(context.Background(), newTestClient(srv), "7")
	if !strings.Contains(gotQuery, "status=current") || !strings.Contains(gotQuery, "status=archived") {
		t.Errorf("pages must be fetched with status=current and status=archived, got %q", gotQuery)
	}
}

// The search API caps `limit` below the requested 250. The old code stopped
// as soon as a page had fewer than 250 results; the next link must be followed.
func TestFetchContentIDsByType_FollowsNextWhenLimitIsCapped(t *testing.T) {
	const total = 5
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		var results []map[string]any
		for i := start; i < start+2 && i < total; i++ {
			results = append(results, map[string]any{"content": map[string]any{"id": fmt.Sprintf("f%d", i), "type": "folder"}})
		}
		links := map[string]any{"base": "https://x.atlassian.net/wiki", "context": "/wiki"}
		if start+2 < total {
			// v1 next links are relative to the /wiki context.
			links["next"] = fmt.Sprintf("/rest/api/search?cql=x&limit=2&cursor=%d", start+2)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": results, "start": 0, "limit": 2, "size": len(results), "totalSize": total, "_links": links,
		})
	}))
	defer srv.Close()

	refs, missing, err := api.FetchContentIDsByType(context.Background(), newTestClient(srv), "KB", "folder")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != total || missing != 0 {
		t.Errorf("got %d refs (missing %d), want %d", len(refs), missing, total)
	}
	for _, p := range paths {
		if p != "/wiki/rest/api/search" {
			t.Errorf("next link not resolved against /wiki: requested %s", p)
		}
	}
}

func TestFetchContentIDsByType_ReportsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results":   []map[string]any{{"content": map[string]any{"id": "w1"}}},
			"limit":     25,
			"totalSize": 3,
			"_links":    map[string]any{},
		})
	}))
	defer srv.Close()

	_, missing, err := api.FetchContentIDsByType(context.Background(), newTestClient(srv), "KB", "whiteboard")
	if err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Errorf("missing = %d, want 2", missing)
	}
}

// Templates were fetched with a single request. Without next links, a full
// page must lead to the next offset.
func TestFetchTemplates_PagesByOffsetWithoutNextLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/template/page") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "limit": 2})
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		var results []map[string]any
		for i := start; i < start+2 && i < 3; i++ {
			results = append(results, map[string]any{"templateId": fmt.Sprintf("t%d", i), "name": "T"})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "start": start, "limit": 2, "size": len(results)})
	}))
	defer srv.Close()

	tpls, err := api.FetchTemplates(context.Background(), newTestClient(srv), "KB")
	if err != nil {
		t.Fatal(err)
	}
	if len(tpls) != 3 {
		t.Errorf("got %d templates, want 3", len(tpls))
	}
	if !strings.Contains(string(tpls[0].Raw), `"templateId"`) {
		t.Errorf("template raw object missing: %s", tpls[0].Raw)
	}
}

func TestFetchAllV1_StartIgnoredErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": "same"}}, "limit": 1})
	}))
	defer srv.Close()

	if _, _, err := api.FetchAllV1(context.Background(), newTestClient(srv), "/wiki/rest/api/template/page"); err == nil {
		t.Fatal("expected an error when every page repeats the first one")
	}
}

// A next link may only move within the v1 REST API; the host is never used.
func TestFetchAllV1_RejectsNextLinkOutsideV1API(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": "1"}},
			"_links":  map[string]any{"next": "https://evil.example/wiki/api/v2/spaces"},
		})
	}))
	defer srv.Close()

	if _, _, err := api.FetchAllV1(context.Background(), newTestClient(srv), "/wiki/rest/api/search?cql=x"); err == nil {
		t.Fatal("expected an error for a next link leaving the v1 REST API")
	}
}

func TestFetchAll_RepeatedNextErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": "1"}},
			"_links":  map[string]any{"next": "/wiki/api/v2/spaces"},
		})
	}))
	defer srv.Close()

	if _, err := api.FetchAll(context.Background(), newTestClient(srv), "/wiki/api/v2/spaces"); err == nil {
		t.Fatal("expected an error when the next link points back to the current page")
	}
}

// v2 list endpoints return root comments only; replies must be fetched via
// /children and nested under "replies".
func TestFetchCommentTree_IncludesReplies(t *testing.T) {
	children := map[string][]map[string]any{
		"c1": {{"id": "c2", "body": map[string]any{"storage": map[string]any{"value": "reply"}}}},
		"c2": {{"id": "c3"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var results []map[string]any
		switch {
		case r.URL.Path == "/wiki/api/v2/pages/10/footer-comments":
			results = []map[string]any{{"id": "c1"}}
		case r.URL.Path == "/wiki/api/v2/pages/10/inline-comments":
			results = []map[string]any{{"id": "i1", "resolutionStatus": "resolved",
				"properties": map[string]any{"inlineOriginalSelection": "quoted text"}}}
		case strings.HasSuffix(r.URL.Path, "/children"):
			id := strings.Split(r.URL.Path, "/")[5]
			results = children[id]
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "_links": map[string]any{}})
	}))
	defer srv.Close()

	tree, n, err := api.FetchCommentTree(context.Background(), newTestClient(srv), api.ContainerPage, "10")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("total comments = %d, want 4 (c1, c2, c3, i1)", n)
	}
	var footer []struct {
		ID      string `json:"id"`
		Replies []struct {
			ID      string `json:"id"`
			Replies []struct {
				ID string `json:"id"`
			} `json:"replies"`
		} `json:"replies"`
	}
	b, _ := json.Marshal(tree.Footer)
	json.Unmarshal(b, &footer)
	if len(footer) != 1 || len(footer[0].Replies) != 1 || footer[0].Replies[0].ID != "c2" ||
		len(footer[0].Replies[0].Replies) != 1 || footer[0].Replies[0].Replies[0].ID != "c3" {
		t.Errorf("reply thread not nested: %s", b)
	}
	// Inline comment anchors and resolution state must survive (raw object).
	if !strings.Contains(string(tree.Inline[0]), "quoted text") || !strings.Contains(string(tree.Inline[0]), "resolved") {
		t.Errorf("inline comment fields lost: %s", tree.Inline[0])
	}
}

// Downloads may follow redirects on the same host or to Atlassian over HTTPS,
// but never to an arbitrary host.
func TestDownloadPath_RedirectPolicy(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "should not be reached")
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/file", http.StatusFound)
		case "/file":
			io.WriteString(w, "content")
		case "/foreign":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)

	rc, err := c.DownloadPath(context.Background(), "/same")
	if err != nil {
		t.Fatalf("same-host redirect refused: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "content" {
		t.Errorf("downloaded %q", b)
	}

	if _, err := c.DownloadPath(context.Background(), "/foreign"); err == nil {
		t.Error("redirect to a foreign host must be refused")
	}
}

func TestAttachmentDownloadPath(t *testing.T) {
	if got := api.AttachmentDownloadPath("123", "att456"); got != "/wiki/rest/api/content/123/child/attachment/att456/download" {
		t.Errorf("got %s", got)
	}
}
