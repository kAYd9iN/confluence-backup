package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// validDomain matches Atlassian Cloud domains.
var validDomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-\.]{0,253}$`)

// ValidateDomain returns an error if domain contains unsafe characters.
func ValidateDomain(domain string) error {
	if !validDomain.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	return nil
}

// --- Types ---

type Space struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

type SpacePermission struct {
	ID        string          `json:"id"`
	Principal json.RawMessage `json:"principal"`
	Operation json.RawMessage `json:"operation"`
}

type SpaceProperty struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value"`
	Version json.RawMessage `json:"version"`
}

type SpaceDetail struct {
	Space
	Permissions []SpacePermission `json:"permissions,omitempty"`
	Properties  []SpaceProperty   `json:"properties,omitempty"`
}

type PageVersion struct {
	Number    int    `json:"number"`
	CreatedAt string `json:"createdAt"`
	AuthorID  string `json:"authorId,omitempty"`
}

type Label struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

type Page struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	SpaceID    string      `json:"spaceId"`
	ParentID   string      `json:"parentId,omitempty"`
	ParentType string      `json:"parentType,omitempty"`
	Status     string      `json:"status"`
	Version    PageVersion `json:"version"`
	Body       struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Labels []Label `json:"labels,omitempty"`
}

type BlogPost struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	SpaceID string      `json:"spaceId"`
	Status  string      `json:"status"`
	Version PageVersion `json:"version"`
	Body    struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
}

type Comment struct {
	ID   string `json:"id"`
	Body struct {
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"body"`
	Version json.RawMessage `json:"version"`
}

type Comments struct {
	Footer []Comment `json:"footer"`
	Inline []Comment `json:"inline"`
}

type Attachment struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	FileSize  int64  `json:"fileSize"`
	MediaType string `json:"mediaType"`
	// Links contains the relative download URL; build the full URL via client.BaseURL().
	Links struct {
		Download string `json:"download"`
	} `json:"_links"`
}

type Template struct {
	TemplateID   string `json:"templateId"`
	Name         string `json:"name"`
	TemplateType string `json:"templateType"`
	Body         struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AccountType string `json:"accountType"`
}

// --- Fetch functions ---

// FetchSpaces returns all visible spaces.
func FetchSpaces(ctx context.Context, client *Client) ([]Space, error) {
	items, err := FetchAll(ctx, client, "/wiki/api/v2/spaces?limit=250")
	if err != nil {
		return nil, fmt.Errorf("fetch spaces: %w", err)
	}
	spaces := make([]Space, 0, len(items))
	for _, raw := range items {
		var s Space
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parse space: %w", err)
		}
		spaces = append(spaces, s)
	}
	return spaces, nil
}

// FetchSpaceDetail fetches permissions and properties for one space.
// Uses both v2 (permissions) and v1 (properties) APIs.
func FetchSpaceDetail(ctx context.Context, client *Client, space Space) (SpaceDetail, error) {
	detail := SpaceDetail{Space: space}

	// Permissions (v2)
	permItems, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/permissions?limit=250", space.ID))
	if err != nil {
		return detail, fmt.Errorf("fetch permissions for space %s: %w", space.Key, err)
	}
	for _, raw := range permItems {
		var p SpacePermission
		if err := json.Unmarshal(raw, &p); err == nil {
			detail.Permissions = append(detail.Permissions, p)
		}
	}

	// Properties (v1 — not yet in v2)
	propBody, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/space/%s/property?expand=value,version&limit=200", space.Key))
	if err == nil {
		var resp struct {
			Results []SpaceProperty `json:"results"`
		}
		if json.Unmarshal(propBody, &resp) == nil {
			detail.Properties = resp.Results
		}
	}
	// Properties fetch failure is non-fatal — continue without them.

	return detail, nil
}

// FetchPages returns all pages in a space with HTML body.
func FetchPages(ctx context.Context, client *Client, spaceID string) ([]Page, error) {
	path := fmt.Sprintf(
		"/wiki/api/v2/pages?spaceId=%s&body-format=view&limit=250&status=current", spaceID)
	items, err := FetchAll(ctx, client, path)
	if err != nil {
		return nil, fmt.Errorf("fetch pages for space %s: %w", spaceID, err)
	}
	pages := make([]Page, 0, len(items))
	for _, raw := range items {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse page: %w", err)
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// FetchBlogPosts returns all blog posts in a space with HTML body.
func FetchBlogPosts(ctx context.Context, client *Client, spaceID string) ([]BlogPost, error) {
	path := fmt.Sprintf(
		"/wiki/api/v2/blogposts?spaceId=%s&body-format=view&limit=250&status=current", spaceID)
	items, err := FetchAll(ctx, client, path)
	if err != nil {
		return nil, fmt.Errorf("fetch blogposts for space %s: %w", spaceID, err)
	}
	posts := make([]BlogPost, 0, len(items))
	for _, raw := range items {
		var p BlogPost
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse blogpost: %w", err)
		}
		posts = append(posts, p)
	}
	return posts, nil
}

// FetchComments fetches footer and inline comments for a page.
func FetchComments(ctx context.Context, client *Client, pageID string) (Comments, error) {
	var c Comments

	footer, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/footer-comments?body-format=view&limit=250", pageID))
	if err != nil {
		return c, fmt.Errorf("fetch footer comments for page %s: %w", pageID, err)
	}
	for _, raw := range footer {
		var comment Comment
		if json.Unmarshal(raw, &comment) == nil {
			c.Footer = append(c.Footer, comment)
		}
	}

	inline, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/inline-comments?body-format=view&limit=250", pageID))
	if err != nil {
		return c, fmt.Errorf("fetch inline comments for page %s: %w", pageID, err)
	}
	for _, raw := range inline {
		var comment Comment
		if json.Unmarshal(raw, &comment) == nil {
			c.Inline = append(c.Inline, comment)
		}
	}

	return c, nil
}

// FetchAttachmentMeta returns attachment metadata for a page (no file download).
func FetchAttachmentMeta(ctx context.Context, client *Client, pageID string) ([]Attachment, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/pages/%s/attachments?limit=250", pageID))
	if err != nil {
		return nil, fmt.Errorf("fetch attachments for page %s: %w", pageID, err)
	}
	atts := make([]Attachment, 0, len(items))
	for _, raw := range items {
		var a Attachment
		if err := json.Unmarshal(raw, &a); err == nil {
			atts = append(atts, a)
		}
	}
	return atts, nil
}

// FetchTemplates returns space-level templates (v1 API).
func FetchTemplates(ctx context.Context, client *Client, spaceKey string) ([]Template, error) {
	body, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/template?spaceKey=%s&limit=200", spaceKey))
	if err != nil {
		return nil, fmt.Errorf("fetch templates for space %s: %w", spaceKey, err)
	}
	var resp struct {
		Results []Template `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return resp.Results, nil
}

// FetchUserProfile fetches a single user profile by account ID (v1 API).
func FetchUserProfile(ctx context.Context, client *Client, accountID string) (User, error) {
	body, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/user?accountId=%s&expand=email", accountID))
	if err != nil {
		return User{}, fmt.Errorf("fetch user %s: %w", accountID, err)
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, fmt.Errorf("parse user %s: %w", accountID, err)
	}
	return u, nil
}
