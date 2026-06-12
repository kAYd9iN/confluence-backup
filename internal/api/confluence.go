package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
)

// validDomain matches hostnames: alphanumeric, hyphens, dots, max 253 chars.
var validDomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-\.]{0,253}$`)

// privateHostPattern blocks loopback and RFC-1918 addresses to prevent SSRF.
var privateHostPattern = regexp.MustCompile(
	`(?i)^(localhost|127\.|10\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.168\.|169\.254\.|::1|0\.0\.0\.0)`,
)

// ValidateDomain returns an error if domain contains unsafe characters or
// resolves to a private/loopback address range (SSRF protection).
func ValidateDomain(domain string) error {
	if !validDomain.MatchString(domain) {
		return fmt.Errorf("invalid domain %q: must be a valid hostname", domain)
	}
	if privateHostPattern.MatchString(domain) {
		return fmt.Errorf("domain %q matches a private/loopback address range — refused to prevent SSRF", domain)
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
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
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
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

type Comment struct {
	ID   string `json:"id"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
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

// SpaceLabels groups space-level labels and labels used on content in the space.
type SpaceLabels struct {
	Space   []Label `json:"space"`
	Content []Label `json:"content"`
}

// Task is an inline task (action item) from pages or blog posts.
type Task struct {
	ID          string `json:"id"`
	LocalID     string `json:"localId,omitempty"`
	SpaceID     string `json:"spaceId,omitempty"`
	PageID      string `json:"pageId,omitempty"`
	BlogPostID  string `json:"blogPostId,omitempty"`
	Status      string `json:"status"`
	CreatedBy   string `json:"createdBy,omitempty"`
	AssignedTo  string `json:"assignedTo,omitempty"`
	CompletedBy string `json:"completedBy,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	DueAt       string `json:"dueAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Body        struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

// ContentRef identifies a content item found via CQL search.
type ContentRef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
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

// FetchSpaceDetail fetches permissions and properties for one space (v2 API).
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

	// Properties (v2)
	propItems, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/properties?limit=250", space.ID))
	if err == nil {
		for _, raw := range propItems {
			var p SpaceProperty
			if json.Unmarshal(raw, &p) == nil {
				detail.Properties = append(detail.Properties, p)
			}
		}
	}
	// Properties fetch failure is non-fatal — continue without them.

	return detail, nil
}

// FetchPages returns all pages in a space with HTML body.
func FetchPages(ctx context.Context, client *Client, spaceID string) ([]Page, error) {
	path := fmt.Sprintf(
		"/wiki/api/v2/spaces/%s/pages?body-format=storage&limit=250&status=current", spaceID)
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
		"/wiki/api/v2/spaces/%s/blogposts?body-format=storage&limit=250&status=current", spaceID)
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
		fmt.Sprintf("/wiki/api/v2/pages/%s/footer-comments?body-format=storage&limit=250", pageID))
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
		fmt.Sprintf("/wiki/api/v2/pages/%s/inline-comments?body-format=storage&limit=250", pageID))
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

// FetchTemplates returns space-level content and blueprint templates (v1 API).
// The combined /wiki/rest/api/template endpoint is no longer documented;
// /template/page and /template/blueprint replace it.
func FetchTemplates(ctx context.Context, client *Client, spaceKey string) ([]Template, error) {
	var all []Template
	for _, kind := range []string{"page", "blueprint"} {
		body, err := client.Get(ctx,
			fmt.Sprintf("/wiki/rest/api/template/%s?spaceKey=%s&limit=200", kind, spaceKey))
		if err != nil {
			return all, fmt.Errorf("fetch %s templates for space %s: %w", kind, spaceKey, err)
		}
		var resp struct {
			Results []Template `json:"results"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return all, fmt.Errorf("parse %s templates for space %s: %w", kind, spaceKey, err)
		}
		all = append(all, resp.Results...)
	}
	return all, nil
}

// FetchSpaceLabels returns space-level labels and content labels for a space.
func FetchSpaceLabels(ctx context.Context, client *Client, spaceID string) (SpaceLabels, error) {
	var sl SpaceLabels

	spaceItems, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/labels?limit=250", spaceID))
	if err != nil {
		return sl, fmt.Errorf("fetch space labels for space %s: %w", spaceID, err)
	}
	for _, raw := range spaceItems {
		var l Label
		if json.Unmarshal(raw, &l) == nil {
			sl.Space = append(sl.Space, l)
		}
	}

	contentItems, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/content/labels?limit=250", spaceID))
	if err != nil {
		return sl, fmt.Errorf("fetch content labels for space %s: %w", spaceID, err)
	}
	for _, raw := range contentItems {
		var l Label
		if json.Unmarshal(raw, &l) == nil {
			sl.Content = append(sl.Content, l)
		}
	}

	return sl, nil
}

// FetchTasks returns all inline tasks in a space.
func FetchTasks(ctx context.Context, client *Client, spaceID string) ([]Task, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/tasks?space-id=%s&body-format=storage&limit=250", spaceID))
	if err != nil {
		return nil, fmt.Errorf("fetch tasks for space %s: %w", spaceID, err)
	}
	tasks := make([]Task, 0, len(items))
	for _, raw := range items {
		var t Task
		if err := json.Unmarshal(raw, &t); err == nil {
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

// FetchCustomContent returns custom content (app-defined types) in a space.
// The payload shape is app-specific, so items are kept as raw JSON.
func FetchCustomContent(ctx context.Context, client *Client, spaceID string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/custom-content?limit=250", spaceID))
	if err != nil {
		return nil, fmt.Errorf("fetch custom content for space %s: %w", spaceID, err)
	}
	return items, nil
}

// smartContentTypes are v2 content types without list endpoints — they are
// discovered per space via CQL search, then fetched individually by ID.
// Maps CQL type name → v2 URL path segment.
var smartContentTypes = map[string]string{
	"whiteboard": "whiteboards",
	"database":   "databases",
	"folder":     "folders",
	"embed":      "embeds",
}

// SmartContentTypes returns the discoverable content types in stable order.
func SmartContentTypes() []string {
	return []string{"whiteboard", "database", "folder", "embed"}
}

// searchMaxPages bounds v1 search pagination (250 items/page → 10k items).
const searchMaxPages = 40

// FetchContentIDsByType discovers content of a given type (whiteboard,
// database, folder, embed) in a space via the v1 CQL search API, which is
// the documented way to enumerate these types — v2 has no list endpoints.
func FetchContentIDsByType(ctx context.Context, client *Client, spaceKey, contentType string) ([]ContentRef, error) {
	if _, ok := smartContentTypes[contentType]; !ok {
		return nil, fmt.Errorf("unsupported content type %q", contentType)
	}
	var refs []ContentRef
	cql := url.QueryEscape(fmt.Sprintf("space = %q and type = %s", spaceKey, contentType))
	for page := 0; page < searchMaxPages; page++ {
		body, err := client.Get(ctx, fmt.Sprintf(
			"/wiki/rest/api/search?cql=%s&limit=250&start=%d", cql, page*250))
		if err != nil {
			return refs, fmt.Errorf("search %s in space %s: %w", contentType, spaceKey, err)
		}
		var resp struct {
			Results []struct {
				Content ContentRef `json:"content"`
			} `json:"results"`
			Size int `json:"size"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return refs, fmt.Errorf("parse search results for %s in space %s: %w", contentType, spaceKey, err)
		}
		for _, r := range resp.Results {
			if r.Content.ID != "" {
				refs = append(refs, r.Content)
			}
		}
		if len(resp.Results) < 250 {
			break
		}
	}
	return refs, nil
}

// FetchContentItem fetches a single whiteboard, database, folder, or embed
// by ID. The v2 API exposes metadata only for these types (no exportable
// body), so the full payload is kept as raw JSON.
func FetchContentItem(ctx context.Context, client *Client, contentType, id string) (json.RawMessage, error) {
	segment, ok := smartContentTypes[contentType]
	if !ok {
		return nil, fmt.Errorf("unsupported content type %q", contentType)
	}
	body, err := client.Get(ctx, fmt.Sprintf("/wiki/api/v2/%s/%s", segment, url.PathEscape(id)))
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", contentType, id, err)
	}
	return json.RawMessage(body), nil
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
