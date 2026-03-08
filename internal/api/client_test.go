package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	c := api.NewClient(srv.URL, "test-token")
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

	c := api.NewClient(srv.URL, "tok")
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

	c := api.NewClient(srv.URL, "tok")
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
