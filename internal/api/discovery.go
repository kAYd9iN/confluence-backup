package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AccessibleResourcesURL is the Atlassian endpoint that lists sites a token can access.
// Overridable in tests.
var AccessibleResourcesURL = "https://api.atlassian.com/oauth/token/accessible-resources"

// GatewayURL returns the Atlassian API Gateway base URL for a given cloudID.
func GatewayURL(cloudID string) string {
	return "https://api.atlassian.com/ex/confluence/" + cloudID
}

type accessibleResource struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// DiscoverCloudID calls the Atlassian accessible-resources endpoint and returns
// the cloudID for the site matching the given domain.
func DiscoverCloudID(token, domain string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, AccessibleResourcesURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("accessible-resources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("accessible-resources: HTTP %d", resp.StatusCode)
	}

	var resources []accessibleResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return "", fmt.Errorf("decode accessible-resources: %w", err)
	}

	// Normalise domain for comparison: strip scheme, trailing slash.
	want := strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	want = strings.TrimSuffix(want, "/")

	for _, r := range resources {
		siteHost := strings.TrimPrefix(strings.TrimPrefix(r.URL, "https://"), "http://")
		siteHost = strings.TrimSuffix(siteHost, "/")
		if siteHost == want {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no accessible site found for domain %q", domain)
}
