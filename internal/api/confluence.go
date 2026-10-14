package api

import (
	"bytes"
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

// Backups keep every API object as the raw JSON Confluence returned, so a
// restore or importer has all fields (ordering, authorship, inline-comment
// anchors, …). The typed fields below are only what the backup logic itself
// needs to read; Raw carries the complete object.

// --- Types ---

type Space struct {
	ID         string          `json:"id"`
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	HomepageID string          `json:"homepageId,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

type PageVersion struct {
	Number    int    `json:"number"`
	CreatedAt string `json:"createdAt"`
	AuthorID  string `json:"authorId,omitempty"`
}

type storageBody struct {
	Storage struct {
		Value string `json:"value"`
	} `json:"storage"`
}

// Page is a page or blog post (both share these fields; blog posts have no parent).
type Page struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	SpaceID    string          `json:"spaceId"`
	ParentID   string          `json:"parentId,omitempty"`
	ParentType string          `json:"parentType,omitempty"`
	Status     string          `json:"status"`
	AuthorID   string          `json:"authorId,omitempty"`
	OwnerID    string          `json:"ownerId,omitempty"`
	CreatedAt  string          `json:"createdAt,omitempty"`
	Version    PageVersion     `json:"version"`
	Body       storageBody     `json:"body"`
	Raw        json.RawMessage `json:"-"`
}

// BlogPost has the same shape as a page.
type BlogPost = Page

// Attachment is the part of an attachment record the backup needs to
// download the file; the complete record is kept in Raw.
type Attachment struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	FileSize     int64  `json:"fileSize"`
	MediaType    string `json:"mediaType"`
	DownloadLink string `json:"downloadLink"`
	Links        struct {
		Download string `json:"download"`
	} `json:"_links"`
	Raw json.RawMessage `json:"-"`
}

// Template is the part of a template record used to name its backup file.
type Template struct {
	TemplateID string          `json:"templateId"`
	Name       string          `json:"name"`
	Raw        json.RawMessage `json:"-"`
}

// ContentRef identifies a content item found via CQL search.
type ContentRef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// CommentTree holds the footer and inline comments of a page or blog post.
// Each comment is the raw API object plus a "replies" array holding its
// reply thread (recursively).
type CommentTree struct {
	Footer []json.RawMessage `json:"footer"`
	Inline []json.RawMessage `json:"inline"`
}

// --- Spaces ---

// FetchSpaces returns all visible spaces, each with its raw API object
// (including homepageId and the plain-text description; "view" is not supported
// through the API gateway).
func FetchSpaces(ctx context.Context, client *Client) ([]Space, error) {
	items, err := FetchAll(ctx, client, "/wiki/api/v2/spaces?limit=250&description-format=plain")
	if err != nil {
		return nil, fmt.Errorf("fetch spaces: %w", err)
	}
	spaces := make([]Space, 0, len(items))
	for _, raw := range items {
		var s Space
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parse space: %w", err)
		}
		s.Raw = raw
		spaces = append(spaces, s)
	}
	return spaces, nil
}

// FetchSpacePermissions returns the raw permission entries of a space.
func FetchSpacePermissions(ctx context.Context, client *Client, spaceID string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/permissions?limit=250", url.PathEscape(spaceID)))
	if err != nil {
		return nil, fmt.Errorf("fetch permissions for space %s: %w", spaceID, err)
	}
	return items, nil
}

// FetchSpaceProperties returns the raw content properties of a space.
func FetchSpaceProperties(ctx context.Context, client *Client, spaceID string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/properties?limit=250", url.PathEscape(spaceID)))
	if err != nil {
		return nil, fmt.Errorf("fetch properties for space %s: %w", spaceID, err)
	}
	return items, nil
}

// --- Pages and blog posts ---

func fetchContent(ctx context.Context, client *Client, path, what string) ([]Page, error) {
	items, err := FetchAll(ctx, client, path)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", what, err)
	}
	out := make([]Page, 0, len(items))
	for _, raw := range items {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse %s: %w", what, err)
		}
		p.Raw = raw
		out = append(out, p)
	}
	return out, nil
}

// FetchPages returns every current AND archived page of a space with its
// storage-format body. (Archived pages are real content; the API includes
// them by default, the previous `status=current` filter dropped them.)
func FetchPages(ctx context.Context, client *Client, spaceID string) ([]Page, error) {
	return fetchContent(ctx, client, fmt.Sprintf(
		"/wiki/api/v2/spaces/%s/pages?body-format=storage&limit=250&status=current&status=archived",
		url.PathEscape(spaceID)), "pages for space "+spaceID)
}

// FetchBlogPosts returns all current blog posts of a space with storage-format body.
func FetchBlogPosts(ctx context.Context, client *Client, spaceID string) ([]BlogPost, error) {
	return fetchContent(ctx, client, fmt.Sprintf(
		"/wiki/api/v2/spaces/%s/blogposts?body-format=storage&limit=250&status=current",
		url.PathEscape(spaceID)), "blog posts for space "+spaceID)
}

// Container is the v2 path segment of a content type that owns comments,
// attachments, labels, properties and versions.
type Container string

const (
	ContainerPage     Container = "pages"
	ContainerBlogPost Container = "blogposts"
)

// FetchContentList fetches a raw sub-collection of a page or blog post:
// "labels", "properties", "versions" or "attachments".
func FetchContentList(ctx context.Context, client *Client, c Container, id, sub string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/%s/%s/%s?limit=250", c, url.PathEscape(id), sub))
	if err != nil {
		return nil, fmt.Errorf("fetch %s of %s %s: %w", sub, c, id, err)
	}
	return items, nil
}

// FetchAttachments returns the attachments of a page or blog post.
func FetchAttachments(ctx context.Context, client *Client, c Container, id string) ([]Attachment, error) {
	items, err := FetchContentList(ctx, client, c, id, "attachments")
	if err != nil {
		return nil, err
	}
	atts := make([]Attachment, 0, len(items))
	for _, raw := range items {
		var a Attachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("parse attachment of %s %s: %w", c, id, err)
		}
		a.Raw = raw
		atts = append(atts, a)
	}
	return atts, nil
}

// AttachmentDownloadPath is the documented v1 download endpoint for an
// attachment. It works with Basic auth on the site and with Bearer auth
// through the API gateway, unlike the relative `_links.download` link.
func AttachmentDownloadPath(containerID, attachmentID string) string {
	return fmt.Sprintf("/wiki/rest/api/content/%s/child/attachment/%s/download",
		url.PathEscape(containerID), url.PathEscape(attachmentID))
}

// maxCommentDepth bounds reply-thread recursion.
const maxCommentDepth = 50

// FetchCommentTree returns all footer and inline comments of a page or blog
// post together with their reply threads. The v2 list endpoints return root
// comments only; replies live under /{footer|inline}-comments/{id}/children.
// The second return value is the total number of comments, replies included.
func FetchCommentTree(ctx context.Context, client *Client, c Container, id string) (CommentTree, int, error) {
	var tree CommentTree
	total := 0
	for _, kind := range []string{"footer", "inline"} {
		roots, err := FetchAll(ctx, client, fmt.Sprintf(
			"/wiki/api/v2/%s/%s/%s-comments?body-format=storage&limit=250", c, url.PathEscape(id), kind))
		if err != nil {
			return tree, total, fmt.Errorf("fetch %s comments of %s %s: %w", kind, c, id, err)
		}
		withReplies, n, err := attachReplies(ctx, client, kind, roots, 0)
		if err != nil {
			return tree, total, err
		}
		total += n
		if kind == "footer" {
			tree.Footer = withReplies
		} else {
			tree.Inline = withReplies
		}
	}
	return tree, total, nil
}

// attachReplies adds a "replies" array to each comment, fetched recursively.
func attachReplies(ctx context.Context, client *Client, kind string, comments []json.RawMessage, depth int) ([]json.RawMessage, int, error) {
	if depth > maxCommentDepth {
		return nil, 0, fmt.Errorf("%s comment thread deeper than %d levels", kind, maxCommentDepth)
	}
	out := make([]json.RawMessage, 0, len(comments))
	total := 0
	for _, raw := range comments {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, 0, fmt.Errorf("parse %s comment: %w", kind, err)
		}
		var id string
		if err := json.Unmarshal(obj["id"], &id); err != nil || id == "" {
			return nil, 0, fmt.Errorf("%s comment without id", kind)
		}
		children, err := FetchAll(ctx, client, fmt.Sprintf(
			"/wiki/api/v2/%s-comments/%s/children?body-format=storage&limit=250", kind, url.PathEscape(id)))
		if err != nil {
			return nil, 0, fmt.Errorf("fetch replies to %s comment %s: %w", kind, id, err)
		}
		replies, n, err := attachReplies(ctx, client, kind, children, depth+1)
		if err != nil {
			return nil, 0, err
		}
		if obj["replies"], err = marshalNoEscape(replies); err != nil {
			return nil, 0, err
		}
		merged, err := marshalNoEscape(obj)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, merged)
		total += 1 + n
	}
	return out, total, nil
}

// --- Templates, labels, tasks, custom content ---

// FetchTemplates returns all page and blueprint templates of a space (v1 API),
// following pagination. The combined /wiki/rest/api/template endpoint is no
// longer documented; /template/page and /template/blueprint replace it.
func FetchTemplates(ctx context.Context, client *Client, spaceKey string) ([]Template, error) {
	var all []Template
	for _, kind := range []string{"page", "blueprint"} {
		items, _, err := FetchAllV1(ctx, client, fmt.Sprintf(
			"/wiki/rest/api/template/%s?spaceKey=%s&limit=200", kind, url.QueryEscape(spaceKey)))
		if err != nil {
			return all, fmt.Errorf("fetch %s templates for space %s: %w", kind, spaceKey, err)
		}
		for _, raw := range items {
			var t Template
			if err := json.Unmarshal(raw, &t); err != nil {
				return all, fmt.Errorf("parse %s template for space %s: %w", kind, spaceKey, err)
			}
			t.Raw = raw
			all = append(all, t)
		}
	}
	return all, nil
}

// SpaceLabels groups space-level labels and labels used on content in the space.
type SpaceLabels struct {
	Space   []json.RawMessage `json:"space"`
	Content []json.RawMessage `json:"content"`
}

// FetchSpaceLabels returns space-level labels and content labels for a space.
func FetchSpaceLabels(ctx context.Context, client *Client, spaceID string) (SpaceLabels, error) {
	var sl SpaceLabels
	var err error
	if sl.Space, err = FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/labels?limit=250", url.PathEscape(spaceID))); err != nil {
		return sl, fmt.Errorf("fetch space labels for space %s: %w", spaceID, err)
	}
	if sl.Content, err = FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/content/labels?limit=250", url.PathEscape(spaceID))); err != nil {
		return sl, fmt.Errorf("fetch content labels for space %s: %w", spaceID, err)
	}
	return sl, nil
}

// FetchTasks returns all inline tasks in a space (raw).
func FetchTasks(ctx context.Context, client *Client, spaceID string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/tasks?space-id=%s&body-format=storage&limit=250", url.QueryEscape(spaceID)))
	if err != nil {
		return nil, fmt.Errorf("fetch tasks for space %s: %w", spaceID, err)
	}
	return items, nil
}

// FetchCustomContent returns custom content (app-defined types) in a space.
// The payload shape is app-specific, so items are kept as raw JSON.
func FetchCustomContent(ctx context.Context, client *Client, spaceID string) ([]json.RawMessage, error) {
	items, err := FetchAll(ctx, client,
		fmt.Sprintf("/wiki/api/v2/spaces/%s/custom-content?limit=250", url.PathEscape(spaceID)))
	if err != nil {
		return nil, fmt.Errorf("fetch custom content for space %s: %w", spaceID, err)
	}
	return items, nil
}

// --- Whiteboards, databases, folders, embeds ---

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

// FetchContentIDsByType discovers content of a given type (whiteboard,
// database, folder, embed) in a space via the v1 CQL search API, which is
// the documented way to enumerate these types — v2 has no list endpoints.
//
// It follows the search's `_links.next` cursor: the server may cap `limit`
// below the requested 250 ("restricted by fixed system limits"), so a short
// page is not the end of the results. missing is the number of results the
// API announced (totalSize) but did not deliver; 0 when complete.
func FetchContentIDsByType(ctx context.Context, client *Client, spaceKey, contentType string) (refs []ContentRef, missing int, err error) {
	if _, ok := smartContentTypes[contentType]; !ok {
		return nil, 0, fmt.Errorf("unsupported content type %q", contentType)
	}
	cql := url.QueryEscape(fmt.Sprintf("space = %q and type = %s", spaceKey, contentType))
	items, total, err := FetchAllV1(ctx, client, "/wiki/rest/api/search?cql="+cql+"&limit=250")
	if err != nil {
		return nil, 0, fmt.Errorf("search %s in space %s: %w", contentType, spaceKey, err)
	}
	for _, raw := range items {
		var r struct {
			Content ContentRef `json:"content"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, 0, fmt.Errorf("parse search result for %s in space %s: %w", contentType, spaceKey, err)
		}
		if r.Content.ID != "" {
			refs = append(refs, r.Content)
		}
	}
	if total > len(items) {
		missing = total - len(items)
	}
	return refs, missing, nil
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

// --- Users ---

// FetchUserProfile fetches a single user profile by account ID (v1 API, raw).
func FetchUserProfile(ctx context.Context, client *Client, accountID string) (json.RawMessage, error) {
	body, err := client.Get(ctx,
		fmt.Sprintf("/wiki/rest/api/user?accountId=%s&expand=email", url.QueryEscape(accountID)))
	if err != nil {
		return nil, fmt.Errorf("fetch user %s: %w", accountID, err)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("parse user %s: invalid JSON", accountID)
	}
	return json.RawMessage(body), nil
}

// marshalNoEscape is json.Marshal without HTML escaping: comment bodies are
// HTML, and "<p>" stays readable instead of becoming "<p>".
func marshalNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
