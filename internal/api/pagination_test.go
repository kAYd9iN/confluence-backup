package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
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
	// Reduce cap for test speed; restore afterwards.
	orig := api.MaxPages
	api.MaxPages = 5
	defer func() { api.MaxPages = orig }()

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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	c.RetryDelay = 0
	_, err := api.FetchAll(context.Background(), c, "/wiki/api/v2/spaces")
	// Should stop at the safety cap and return an error.
	if err == nil {
		t.Error("expected error when page cap exceeded")
	}
}
