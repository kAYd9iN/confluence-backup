package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	c := api.NewClient(srv.URL, "tok")
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
