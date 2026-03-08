package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	baseURL    string // e.g. "https://your-org.atlassian.net"
	token      string
	limiter    *rate.Limiter
	MaxRetries int
	RetryDelay time.Duration
}

// NewClient creates a client for the given Atlassian domain.
// domain may be a bare hostname ("myorg.atlassian.net") or a full URL
// ("http://127.0.0.1:PORT") — the latter is used by tests.
// token is a Personal Access Token — never logged or returned.
func NewClient(domain, token string) *Client {
	baseURL := domain
	if !strings.Contains(domain, "://") {
		baseURL = "https://" + domain
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    baseURL,
		token:      token,
		limiter:    rate.NewLimiter(rateLimit, rateBurst),
		MaxRetries: maxRetries,
		RetryDelay: 2 * time.Second,
	}
}

// BaseURL returns the Confluence API base (scheme + host only, no path).
func (c *Client) BaseURL() string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return c.baseURL
	}
	return u.Scheme + "://" + u.Host
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

		body, retryable, err := c.doGet(ctx, c.baseURL+path)
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
// The URL must be on the same host as the client domain — SSRF protection.
// Caller MUST close the returned ReadCloser.
// No body size limit — attachments can be large by design.
// Retries on 429 and 5xx, same as Get().
func (c *Client) Download(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	// Validate that the URL stays on the expected host (SSRF protection, #7).
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid download URL: %w", err)
	}
	expected, _ := url.Parse(c.baseURL)
	if parsed.Host != expected.Host {
		return nil, fmt.Errorf("download URL host %q does not match client domain %q — possible SSRF", parsed.Host, expected.Host)
	}

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

		rc, retryable, err := c.doDownload(ctx, rawURL)
		if err == nil {
			return rc, nil
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("download after %d retries: %w", c.MaxRetries, lastErr)
}

func (c *Client) doDownload(ctx context.Context, rawURL string) (io.ReadCloser, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, true, err // network errors are retryable
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close() // #nosec G104 -- Close() error on error path is not actionable
		return nil, true, fmt.Errorf("rate limited (429)")
	case resp.StatusCode >= 500:
		resp.Body.Close() // #nosec G104
		return nil, true, fmt.Errorf("server error HTTP %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close() // #nosec G104
		return nil, false, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	return resp.Body, false, nil
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
