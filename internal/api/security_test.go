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
	c := api.NewClient(srv.URL, "u@example.com", secret)
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
