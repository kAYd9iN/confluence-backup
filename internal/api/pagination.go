package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MaxPages is the safety cap for FetchAll. Override in tests.
var MaxPages = 10_000

// pagedResponse is the envelope returned by all Confluence v2 list endpoints.
type pagedResponse struct {
	Results json.RawMessage `json:"results"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// FetchAll follows cursor pagination and returns all raw JSON items across all pages.
// It is safe to call on any /wiki/api/v2/* list endpoint.
func FetchAll(ctx context.Context, client *Client, path string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	current := path
	for page := 0; page < MaxPages; page++ {
		body, err := client.Get(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("page %d of %s: %w", page+1, path, err)
		}

		var pr pagedResponse
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, fmt.Errorf("parse page %d of %s: %w", page+1, path, err)
		}

		var items []json.RawMessage
		if err := json.Unmarshal(pr.Results, &items); err != nil {
			return nil, fmt.Errorf("parse results page %d: %w", page+1, err)
		}
		all = append(all, items...)

		next := ExtractCursorPath(pr.Links.Next)
		if next == "" {
			return all, nil
		}
		if next == current {
			return nil, fmt.Errorf("%s: next link points back to the current page", path)
		}
		current = next
	}
	return nil, fmt.Errorf("exceeded %d page limit for %s", MaxPages, path)
}

// v1Envelope is one page of a Confluence v1 REST list (CQL search, templates).
type v1Envelope struct {
	Results   json.RawMessage `json:"results"`
	Start     int             `json:"start"`
	Limit     int             `json:"limit"`
	TotalSize *int            `json:"totalSize"`
	Links     struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// FetchAllV1 pages through a v1 REST list endpoint and returns all raw items
// plus the total the API reported in `totalSize` (-1 when absent).
//
// It follows `_links.next`, the documented way to page these endpoints. The
// server may cap `limit` below the requested value ("restricted by fixed
// system limits"), so a page shorter than requested is NOT the end. If a
// response has no next link but its page was full (size == limit), it falls
// back to offset paging via `start`.
func FetchAllV1(ctx context.Context, client *Client, path string) ([]json.RawMessage, int, error) {
	var all []json.RawMessage
	total := -1
	current := path
	fetched := map[string]bool{}
	var prevFirst json.RawMessage
	for page := 0; page < MaxPages; page++ {
		if fetched[current] {
			return nil, total, fmt.Errorf("%s: pagination loops back to an already fetched page", path)
		}
		fetched[current] = true

		body, err := client.Get(ctx, current)
		if err != nil {
			return nil, total, fmt.Errorf("page %d of %s: %w", page+1, path, err)
		}
		var env v1Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, total, fmt.Errorf("parse page %d of %s: %w", page+1, path, err)
		}
		var items []json.RawMessage
		if len(env.Results) > 0 {
			if err := json.Unmarshal(env.Results, &items); err != nil {
				return nil, total, fmt.Errorf("parse results page %d of %s: %w", page+1, path, err)
			}
		}
		// An endpoint that ignores `start` returns page 1 again.
		if page > 0 && len(items) > 0 && bytes.Equal(items[0], prevFirst) {
			return nil, total, fmt.Errorf("%s: page %d repeats the previous page — paging parameter ignored", path, page+1)
		}
		if len(items) > 0 {
			prevFirst = items[0]
		}
		all = append(all, items...)
		if env.TotalSize != nil {
			total = *env.TotalSize
		}

		switch {
		case env.Links.Next != "":
			if current, err = v1NextPath(env.Links.Next); err != nil {
				return nil, total, err
			}
		case len(items) > 0 && env.Limit > 0 && len(items) >= env.Limit:
			// Full page without a next link: try the next offset.
			if current, err = withStart(current, env.Start+len(items)); err != nil {
				return nil, total, err
			}
		default:
			return all, total, nil
		}
	}
	return nil, total, fmt.Errorf("exceeded %d page limit for %s", MaxPages, path)
}

// v1NextPath turns a v1 `_links.next` value into a request path for the
// client. Only the path and query are used — never a host from the response —
// and the result must stay inside the v1 REST API. Confluence returns next
// links relative to the /wiki context ("/rest/api/search?…"), so the prefix
// is added when missing.
func v1NextPath(next string) (string, error) {
	u, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("unparseable next link: %w", err)
	}
	p := u.Path
	switch {
	case strings.HasPrefix(p, "/wiki/"):
	case strings.Contains(p, "/wiki/"):
		p = p[strings.Index(p, "/wiki/"):] // e.g. a gateway prefix /ex/confluence/{id}/wiki/…
	case strings.HasPrefix(p, "/"):
		p = "/wiki" + p
	default:
		return "", fmt.Errorf("unexpected next link %q", next)
	}
	if !strings.HasPrefix(p, "/wiki/rest/api/") {
		return "", fmt.Errorf("next link %q leaves the v1 REST API", next)
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p, nil
}

// withStart returns path with its `start` query parameter set to start.
func withStart(path string, start int) (string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("start", strconv.Itoa(start))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExtractCursorPath returns the path+query portion of a Confluence _links.next value.
// Confluence returns a relative path (e.g. "/wiki/api/v2/spaces?cursor=abc&limit=50").
// Returns "" if nextLink is empty.
func ExtractCursorPath(nextLink string) string {
	return nextLink // Confluence already returns a relative path — use as-is
}
