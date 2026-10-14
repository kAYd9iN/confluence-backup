// Package xwiki imports a confluence-backup into an XWiki instance through
// XWiki's REST API. It writes ONLY to the XWiki URL it is given and never
// talks to Confluence; the Confluence client (internal/api) stays GET-only.
package xwiki

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrUnauthorized is returned when XWiki rejects the credentials or rights;
// every further request would fail the same way.
var ErrUnauthorized = errors.New("XWiki rejected the request — check XWiki user/password and its rights")

// Client is a minimal XWiki REST client with Basic auth.
type Client struct {
	base     *url.URL // e.g. http://localhost:8080 or https://wiki.example.com/xwiki
	user     string
	password string
	http     *http.Client
	// MaxRetries for 429/5xx and network errors; RetryDelay doubles each time.
	MaxRetries int
	RetryDelay time.Duration
}

// NewClient creates a client for the XWiki at baseURL (scheme, host and the
// context path, e.g. "https://wiki.example.com/xwiki").
func NewClient(baseURL, user, password string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid XWiki URL %q: need http(s)://host[/context]", baseURL)
	}
	return &Client{
		base: u, user: user, password: password,
		http: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: false},
				ResponseHeaderTimeout: 120 * time.Second, // large attachments: no overall timeout
			},
			// Never follow a redirect with the credentials to another host.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("refusing redirect to %s", req.URL.Host)
				}
				return nil
			},
		},
		MaxRetries: 3, RetryDelay: 2 * time.Second,
	}, nil
}

// ContextPath is the URL path XWiki is served under ("" or e.g. "/xwiki").
func (c *Client) ContextPath() string { return strings.TrimRight(c.base.Path, "/") }

// Insecure reports whether credentials would travel unencrypted to a
// non-local host.
func (c *Client) Insecure() bool {
	h := c.base.Hostname()
	return c.base.Scheme == "http" && h != "localhost" && h != "127.0.0.1" && h != "::1"
}

// put sends body to the REST path (below /rest) and accepts any 2xx status.
func (c *Client) put(ctx context.Context, restPath, contentType string, body []byte) error {
	return c.do(ctx, http.MethodPut, restPath, contentType, func() (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
	})
}

// putFile streams the file at path to the REST path (attachments can be
// large, so they are never read into memory). The file is reopened per attempt.
func (c *Client) putFile(ctx context.Context, restPath, contentType, path string) error {
	return c.do(ctx, http.MethodPut, restPath, contentType, func() (io.ReadCloser, int64, error) {
		f, err := os.Open(path) // #nosec G304 -- attachment file inside the backup being imported
		if err != nil {
			return nil, 0, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close() // #nosec G104
			return nil, 0, err
		}
		return f, st.Size(), nil
	})
}

// get returns the status code of a GET on the REST path.
func (c *Client) get(ctx context.Context, restPath string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.restURL(restPath), nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(c.user, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // #nosec G104 -- drain for connection reuse
	resp.Body.Close()                                     // #nosec G104
	return resp.StatusCode, nil
}

func (c *Client) restURL(restPath string) string {
	return c.base.Scheme + "://" + c.base.Host + c.ContextPath() + "/rest" + restPath
}

func (c *Client) do(ctx context.Context, method, restPath, contentType string,
	body func() (io.ReadCloser, int64, error)) error {
	var last error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.RetryDelay * time.Duration(1<<uint(attempt-1))):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		rc, size, err := body()
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.restURL(restPath), rc)
		if err != nil {
			rc.Close() // #nosec G104
			return err
		}
		req.ContentLength = size
		req.SetBasicAuth(c.user, c.password)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/xml")
		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close() // #nosec G104
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("%s %s: HTTP %d", method, restPath, resp.StatusCode)
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return fmt.Errorf("%s %s: HTTP %d: %w", method, restPath, resp.StatusCode, ErrUnauthorized)
		default:
			return fmt.Errorf("%s %s: HTTP %d: %s", method, restPath, resp.StatusCode, strings.TrimSpace(string(msg)))
		}
	}
	return fmt.Errorf("after %d retries: %w", c.MaxRetries, last)
}
