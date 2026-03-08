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
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": "1", "key": "KB", "name": "Knowledge Base", "type": "global", "status": "current"},
			},
			"_links": map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	spaces, err := api.FetchSpaces(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0].Key != "KB" {
		t.Errorf("unexpected spaces: %v", spaces)
	}
}

func TestFetchPages_DecodesStorageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{
					"id": "1", "title": "Test", "spaceId": "42", "status": "current",
					"body": map[string]any{
						"storage": map[string]any{"value": "<p>Hello World</p>"},
					},
				},
			},
			"_links": map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	pages, err := api.FetchPages(context.Background(), c, "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
	if pages[0].Body.Storage.Value != "<p>Hello World</p>" {
		t.Errorf("expected storage body, got %q", pages[0].Body.Storage.Value)
	}
}

func TestFetchPages_UsesStorageBodyFormat(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{},
			"_links":  map[string]any{},
		})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	api.FetchPages(context.Background(), c, "42")
	if gotQuery == "" {
		t.Fatal("no request made")
	}
	if !strings.Contains(gotQuery, "body-format=storage") {
		t.Errorf("expected body-format=storage in query, got: %s", gotQuery)
	}
	if strings.Contains(gotQuery, "body-format=view") {
		t.Errorf("body-format=view must not be used (not supported by API Gateway)")
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
