package api

import (
	"context"
	"encoding/json"
	"fmt"
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
		current = next
	}
	return nil, fmt.Errorf("exceeded %d page limit for %s", MaxPages, path)
}

// ExtractCursorPath returns the path+query portion of a Confluence _links.next value.
// Confluence returns a relative path (e.g. "/wiki/api/v2/spaces?cursor=abc&limit=50").
// Returns "" if nextLink is empty.
func ExtractCursorPath(nextLink string) string {
	return nextLink // Confluence already returns a relative path — use as-is
}
