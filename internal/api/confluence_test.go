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

func TestFetchPages_UsesSpaceScopedEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "_links": map[string]any{}})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	api.FetchPages(context.Background(), c, "99")
	if !strings.HasPrefix(gotPath, "/wiki/api/v2/spaces/99/pages") {
		t.Errorf("expected space-scoped path /wiki/api/v2/spaces/99/pages, got: %s", gotPath)
	}
}

func TestFetchBlogPosts_UsesSpaceScopedEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "_links": map[string]any{}})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	api.FetchBlogPosts(context.Background(), c, "99")
	if !strings.HasPrefix(gotPath, "/wiki/api/v2/spaces/99/blogposts") {
		t.Errorf("expected space-scoped path /wiki/api/v2/spaces/99/blogposts, got: %s", gotPath)
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

func TestFetchSpaceDetail_UsesV2PropertiesEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/properties") {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"key": "theme", "value": map[string]any{"color": "blue"}, "version": map[string]any{"number": 1}},
				},
				"_links": map[string]any{},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "_links": map[string]any{}})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	detail, err := api.FetchSpaceDetail(context.Background(), c,
		api.Space{ID: "123", Key: "KB"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range paths {
		if strings.Contains(p, "/wiki/rest/api/") {
			t.Errorf("v1 endpoint must not be called, got: %s", p)
		}
		if p == "/wiki/api/v2/spaces/123/properties" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected v2 properties path /wiki/api/v2/spaces/123/properties, got: %v", paths)
	}
	if len(detail.Properties) != 1 || detail.Properties[0].Key != "theme" {
		t.Errorf("unexpected properties: %+v", detail.Properties)
	}
}

func TestFetchTemplates_UsesPageAndBlueprintEndpoints(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var results []map[string]any
		switch r.URL.Path {
		case "/wiki/rest/api/template/page":
			results = []map[string]any{{"templateId": "t1", "name": "Meeting Notes", "templateType": "page"}}
		case "/wiki/rest/api/template/blueprint":
			results = []map[string]any{{"templateId": "t2", "name": "Decision", "templateType": "blueprint"}}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "tok")
	templates, err := api.FetchTemplates(context.Background(), c, "KB")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 requests (page + blueprint), got %d: %v", len(paths), paths)
	}
	if len(templates) != 2 || templates[0].TemplateID != "t1" || templates[1].TemplateID != "t2" {
		t.Errorf("unexpected templates: %+v", templates)
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
