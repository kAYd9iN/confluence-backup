package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestDiscoverCloudID_MatchesByDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer auth, got: %s", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "other-cloud-id", "url": "https://other.atlassian.net"},
			{"id": "target-cloud-id", "url": "https://myorg.atlassian.net"},
		})
	}))
	defer srv.Close()

	orig := api.AccessibleResourcesURL
	api.AccessibleResourcesURL = srv.URL
	defer func() { api.AccessibleResourcesURL = orig }()

	cloudID, err := api.DiscoverCloudID("test-token", "myorg.atlassian.net")
	if err != nil {
		t.Fatal(err)
	}
	if cloudID != "target-cloud-id" {
		t.Errorf("expected target-cloud-id, got %q", cloudID)
	}
}

func TestDiscoverCloudID_ReturnsErrorOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	orig := api.AccessibleResourcesURL
	api.AccessibleResourcesURL = srv.URL
	defer func() { api.AccessibleResourcesURL = orig }()

	_, err := api.DiscoverCloudID("tok", "myorg.atlassian.net")
	if err == nil {
		t.Error("expected error on 401")
	}
}

func TestDiscoverCloudID_ErrorWhenDomainNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "some-id", "url": "https://other.atlassian.net"},
		})
	}))
	defer srv.Close()

	orig := api.AccessibleResourcesURL
	api.AccessibleResourcesURL = srv.URL
	defer func() { api.AccessibleResourcesURL = orig }()

	_, err := api.DiscoverCloudID("tok", "myorg.atlassian.net")
	if err == nil {
		t.Error("expected error when domain not found")
	}
}
