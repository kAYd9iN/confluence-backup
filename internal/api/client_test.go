package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
)

func TestNewClientBearer_UsesBearerAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer secret-token" {
			t.Errorf("expected Bearer auth, got: %s", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"results":[],"_links":{}}`))
	}))
	defer srv.Close()

	c := api.NewClientBearer(srv.URL, "secret-token")
	_, err := c.Get(context.Background(), "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_Get_UsesBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user@example.com" || pass != "test-token" {
			t.Errorf("expected Basic auth with email/token, got Authorization: %s", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "user@example.com", "test-token")
	_, err := c.Get(context.Background(), "/wiki/api/v2/spaces")
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_Get_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			t.Errorf("missing Basic auth header")
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "u@example.com", "test-token")
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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
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

	c := api.NewClient(srv.URL, "u@example.com", "tok")
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
