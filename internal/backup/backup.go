package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// controlChars matches ASCII control characters and ANSI escape sequences.
// Used to sanitize API-supplied strings before logging (log injection prevention).
var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]|\x1b\[[0-9;]*[a-zA-Z]`)

func sanitizeLog(s string) string {
	return controlChars.ReplaceAllString(s, "?")
}

const maxTreeDepth = 100 // prevents stack overflow on pathologically deep page trees

// Config controls what gets backed up.
type Config struct {
	Domain             string
	OutputDir          string
	ExcludeSpaces      map[string]bool // space keys to skip
	IncludeAttachments bool
	Timeout            time.Duration
	DryRun             bool
	ToolVersion        string
}

// FormatBackupDir returns the timestamp-based directory name for a backup.
// The time is formatted in its own timezone — callers should pass time.Now()
// which is already in the local timezone, avoiding UTC date mismatches.
func FormatBackupDir(t time.Time) string {
	return t.Format("2006-01-02T15-04-05")
}

// recorder writes backup files and records every outcome in the manifest.
// Anything that could not be backed up becomes a "failed" manifest entry and
// makes the run report an error — a partial backup must never look complete.
type recorder struct {
	w        *storage.Writer
	manifest *Manifest
	dryRun   bool
	failures atomic.Int64
}

// fail records that item (a backup-relative path or a descriptive name) was
// not backed up.
func (r *recorder) fail(item string, err error) {
	r.failures.Add(1)
	r.manifest.AddFailedFile(filepath.ToSlash(item), err)
	slog.Warn("not backed up", "item", sanitizeLog(filepath.ToSlash(item)), "err", sanitizeLog(err.Error()))
}

// write stores data at relPath and adds it to the manifest.
func (r *recorder) write(relPath string, data []byte) bool {
	if r.dryRun {
		return true
	}
	if err := r.w.WriteFile(relPath, data); err != nil {
		r.fail(relPath, err)
		return false
	}
	return r.hash(relPath)
}

func (r *recorder) writeJSON(relPath string, v any) bool {
	data, err := marshalJSON(v, true)
	if err != nil {
		r.fail(relPath, fmt.Errorf("marshal: %w", err))
		return false
	}
	return r.write(relPath, data)
}

func (r *recorder) writeStream(relPath string, src io.Reader) bool {
	if r.dryRun {
		return true
	}
	if err := r.w.WriteBinaryStream(relPath, src); err != nil {
		r.fail(relPath, err)
		return false
	}
	return r.hash(relPath)
}

// hash adds a written file to the manifest. A file that cannot be hashed is
// not verifiable, so it counts as not backed up.
func (r *recorder) hash(relPath string) bool {
	if err := r.manifest.AddFile(filepath.Join(r.w.Dir(), relPath)); err != nil {
		r.fail(relPath, fmt.Errorf("manifest: %w", err))
		return false
	}
	return true
}

// Run executes the full backup and returns the path to the created backup directory.
// It returns an error when anything could not be backed up; the backup
// directory is still returned so the manifest (with the failures) can be signed.
func Run(ctx context.Context, client *api.Client, cfg Config) (string, error) {
	ts := time.Now()
	backupDir := filepath.Join(cfg.OutputDir, FormatBackupDir(ts))

	if cfg.DryRun {
		slog.Info("dry-run mode: no files will be written")
	}

	w, err := storage.NewWriter(backupDir)
	if err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	manifest := NewManifest(cfg.Domain, cfg.ToolVersion, ts)
	// Record file names relative to the backup root so each manifest entry maps
	// to a unique file and can be re-hashed during verify.
	manifest.Root = w.Dir()
	rec := &recorder{w: w, manifest: manifest, dryRun: cfg.DryRun}

	// --- Fetch all spaces ---
	spaces, err := api.FetchSpaces(ctx, client)
	if err != nil {
		return "", fmt.Errorf("fetch spaces: %w", err)
	}
	slog.Info("found spaces", "count", len(spaces))

	// --- Filter excluded spaces ---
	var active []api.Space
	for _, s := range spaces {
		if cfg.ExcludeSpaces[s.Key] {
			slog.Info("skipping space", "key", s.Key)
			continue
		}
		active = append(active, s)
	}

	// --- Collect all user account IDs (authors, owners, commenters) ---
	users := newUserSet()

	// --- Process spaces (3 concurrent) ---
	spaceSem := make(chan struct{}, 3)
	pageSem := make(chan struct{}, 20) // global cap across all spaces

	var wg sync.WaitGroup
	var spaceErrs atomic.Int64
	for _, sp := range active {
		sp := sp
		wg.Add(1)
		go func() {
			defer wg.Done()
			spaceSem <- struct{}{}
			defer func() { <-spaceSem }()

			if err := processSpace(ctx, client, rec, sp, cfg, pageSem, users); err != nil {
				spaceErrs.Add(1)
				rec.fail(filepath.Join("spaces", sp.Key), err)
			}
		}()
	}
	wg.Wait()

	// --- Fetch user profiles ---
	fetchAndWriteUsers(ctx, client, rec, users.ids())

	// --- Write unsigned manifest (signing happens in main after Run returns) ---
	manifestData, _ := json.MarshalIndent(manifest, "", "  ") // manifest format unchanged (signed by main)
	w.WriteFile("backup-manifest.json", manifestData)         // #nosec G104 -- main re-reads, signs and rewrites it

	if n := rec.failures.Load(); n > 0 {
		return w.Dir(), fmt.Errorf("%d item(s) could not be backed up (%d space(s) failed) — see the \"failed\" entries in backup-manifest.json",
			n, spaceErrs.Load())
	}
	return w.Dir(), nil
}

// spaceRecord is what space.json contains: the raw space object plus its
// permissions and properties.
func spaceRecord(sp api.Space, perms, props []json.RawMessage) (map[string]json.RawMessage, error) {
	rec := map[string]json.RawMessage{}
	if len(sp.Raw) > 0 {
		if err := json.Unmarshal(sp.Raw, &rec); err != nil {
			return nil, err
		}
	}
	var err error
	if rec["permissions"], err = marshalJSON(nonNil(perms), false); err != nil {
		return nil, err
	}
	if props != nil {
		if rec["properties"], err = marshalJSON(props, false); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

func nonNil(items []json.RawMessage) []json.RawMessage {
	if items == nil {
		return []json.RawMessage{}
	}
	return items
}

func processSpace(ctx context.Context, client *api.Client, rec *recorder, sp api.Space, cfg Config,
	pageSem chan struct{}, users *userSet) error {

	slog.Info("backing up space", "key", sanitizeLog(sp.Key), "name", sanitizeLog(sp.Name))
	spaceDir := filepath.Join("spaces", storage.SanitizeName(sp.Key))
	users.addFrom(sp.Raw)

	// space.json: space object, permissions (required) and properties.
	perms, err := api.FetchSpacePermissions(ctx, client, sp.ID)
	if err != nil {
		return err
	}
	props, err := api.FetchSpaceProperties(ctx, client, sp.ID)
	if err != nil {
		rec.fail(filepath.Join(spaceDir, "space.json#properties"), err)
	}
	record, err := spaceRecord(sp, perms, props)
	if err != nil {
		return fmt.Errorf("build space record: %w", err)
	}
	if !rec.writeJSON(filepath.Join(spaceDir, "space.json"), record) {
		return fmt.Errorf("space.json could not be written")
	}

	// Templates
	if templates, err := api.FetchTemplates(ctx, client, sp.Key); err != nil {
		rec.fail(filepath.Join(spaceDir, "templates"), err)
	} else {
		for _, tmpl := range templates {
			// Include the unique template ID in the filename: many templates share
			// a display name (e.g. several "Default index page template" blueprints,
			// plus page+blueprint variants), which would otherwise sanitise to the
			// same path and silently overwrite each other.
			tName := safeName(tmpl.Name, 80)
			if tName == "" {
				tName = "template"
			}
			rec.write(filepath.Join(spaceDir, "templates",
				tName+"_"+storage.SanitizeName(tmpl.TemplateID)+".json"), indentRaw(tmpl.Raw))
		}
	}

	// Pages (flat fetch → build tree → write recursively). Current and archived.
	pages, err := api.FetchPages(ctx, client, sp.ID)
	if err != nil {
		return fmt.Errorf("fetch pages: %w", err)
	}
	roots := BuildTree(pages)
	var pageWg sync.WaitGroup
	var writeTree func(nodes []*PageNode, parentRelPath string, depth int)
	writeTree = func(nodes []*PageNode, parentRelPath string, depth int) {
		if depth > maxTreeDepth {
			rec.fail(parentRelPath, fmt.Errorf("page tree deeper than %d levels — subtree skipped", maxTreeDepth))
			return
		}
		for _, node := range nodes {
			node := node
			dirPath := filepath.Join(parentRelPath, node.DirName())
			pageWg.Add(1)
			go func() {
				defer pageWg.Done()
				pageSem <- struct{}{}
				defer func() { <-pageSem }()
				writeContent(ctx, client, rec, api.ContainerPage, node.Page, dirPath, "page.json", cfg, users)
			}()
			writeTree(node.Children, dirPath, depth+1)
		}
	}
	writeTree(roots, filepath.Join(spaceDir, "pages"), 0)
	pageWg.Wait()

	// Blog posts — same treatment as pages (comments, attachments, labels, …).
	posts, err := api.FetchBlogPosts(ctx, client, sp.ID)
	if err != nil {
		rec.fail(filepath.Join(spaceDir, "blog"), err)
	}
	for _, dirName := range blogDirNames(posts) {
		post, name := dirName.post, dirName.name
		pageWg.Add(1)
		go func() {
			defer pageWg.Done()
			pageSem <- struct{}{}
			defer func() { <-pageSem }()
			writeContent(ctx, client, rec, api.ContainerBlogPost, post, filepath.Join(spaceDir, "blog", name), "post.json", cfg, users)
		}()
	}
	pageWg.Wait()

	// Labels, tasks, custom content, and smart content (whiteboards,
	// databases, folders, embeds).
	writeSpaceExtras(ctx, client, rec, sp, spaceDir, users)
	return nil
}

type blogDir struct {
	post api.BlogPost
	name string
}

// blogDirNames assigns each blog post a unique "YYYY-MM-DD_title" directory.
func blogDirNames(posts []api.BlogPost) []blogDir {
	out := make([]blogDir, 0, len(posts))
	seen := map[string]bool{}
	for _, p := range posts {
		date := p.CreatedAt
		if date == "" {
			date = p.Version.CreatedAt
		}
		if len(date) >= 10 {
			date = date[:10]
		} else {
			date = "undated"
		}
		name := date + "_" + safeName(p.Title, 80)
		if seen[name] || safeName(p.Title, 80) == "" {
			name += "_" + storage.SanitizeName(p.ID)
		}
		seen[name] = true
		out = append(out, blogDir{post: p, name: name})
	}
	return out
}

// writeContent backs up one page or blog post into dirPath:
//
//	index.html                 storage-format body (Confluence XHTML)
//	page.json | post.json      the complete API object without its body
//	comments.json              footer + inline comments, each with its reply thread
//	labels.json, properties.json, versions.json
//	attachments/metadata.json  complete attachment records
//	attachments/files/<id>_<name>
//
// Every part that fails is recorded; the others are still written.
func writeContent(ctx context.Context, client *api.Client, rec *recorder, c api.Container,
	p api.Page, dirPath, metaFile string, cfg Config, users *userSet) {

	users.addFrom(p.Raw)

	if !rec.write(filepath.Join(dirPath, "index.html"), []byte(p.Body.Storage.Value)) {
		return
	}
	meta, err := withoutBody(p.Raw)
	if err != nil {
		rec.fail(filepath.Join(dirPath, metaFile), err)
	} else {
		rec.write(filepath.Join(dirPath, metaFile), meta)
	}

	if comments, n, err := api.FetchCommentTree(ctx, client, c, p.ID); err != nil {
		rec.fail(filepath.Join(dirPath, "comments.json"), err)
	} else if n > 0 {
		for _, cm := range append(append([]json.RawMessage{}, comments.Footer...), comments.Inline...) {
			users.addFromTree(cm)
		}
		rec.writeJSON(filepath.Join(dirPath, "comments.json"), comments)
	}

	for _, sub := range []string{"labels", "properties", "versions"} {
		items, err := api.FetchContentList(ctx, client, c, p.ID, sub)
		if err != nil {
			rec.fail(filepath.Join(dirPath, sub+".json"), err)
			continue
		}
		if len(items) > 0 {
			rec.writeJSON(filepath.Join(dirPath, sub+".json"), items)
		}
	}

	atts, err := api.FetchAttachments(ctx, client, c, p.ID)
	if err != nil {
		rec.fail(filepath.Join(dirPath, "attachments"), err)
		return
	}
	if len(atts) == 0 {
		return
	}
	raws := make([]json.RawMessage, len(atts))
	for i, a := range atts {
		raws[i] = a.Raw
		users.addFrom(a.Raw)
	}
	rec.writeJSON(filepath.Join(dirPath, "attachments", "metadata.json"), raws)
	if cfg.IncludeAttachments && !cfg.DryRun {
		downloadAttachments(ctx, client, rec, p.ID, atts, dirPath)
	}
}

// withoutBody returns the indented JSON object minus its "body" field (the
// body is stored separately as index.html).
func withoutBody(raw json.RawMessage) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parse content object: %w", err)
	}
	delete(obj, "body")
	return marshalJSON(obj, true)
}

// indentRaw pretty-prints a raw JSON object (falls back to the input).
func indentRaw(raw json.RawMessage) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, err := marshalJSON(v, true)
	if err != nil {
		return raw
	}
	return b
}

// AttachmentFileName is the file name an attachment is stored under:
// "<attachment id>_<sanitized title>". The ID keeps two attachments whose
// titles sanitize to the same string from overwriting each other.
func AttachmentFileName(a api.Attachment) string {
	name := safeName(a.Title, 120)
	if name == "" {
		name = "file"
	}
	return storage.SanitizeName(a.ID) + "_" + name
}

func downloadAttachments(ctx context.Context, client *api.Client, rec *recorder,
	containerID string, atts []api.Attachment, dirPath string) {
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, att := range atts {
		att := att
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			filePath := filepath.Join(dirPath, "attachments", "files", AttachmentFileName(att))
			rc, err := openAttachment(ctx, client, containerID, att)
			if err != nil {
				rec.fail(filePath, err)
				return
			}
			defer rc.Close()
			rec.writeStream(filePath, rc)
		}()
	}
	wg.Wait()
}

// openAttachment downloads via the documented v1 endpoint and falls back to
// the attachment's own download link (relative to the /wiki context).
func openAttachment(ctx context.Context, client *api.Client, containerID string, att api.Attachment) (io.ReadCloser, error) {
	rc, err := client.DownloadPath(ctx, api.AttachmentDownloadPath(containerID, att.ID))
	if err == nil {
		return rc, nil
	}
	link := att.DownloadLink
	if link == "" {
		link = att.Links.Download
	}
	if len(link) > 0 && link[0] == '/' {
		if rc, err2 := client.DownloadPath(ctx, "/wiki"+link); err2 == nil {
			return rc, nil
		}
	}
	return nil, err
}

// writeSpaceExtras backs up labels, inline tasks, custom content, and smart
// content types (whiteboards, databases, folders, embeds) for one space.
func writeSpaceExtras(ctx context.Context, client *api.Client, rec *recorder, sp api.Space, spaceDir string, users *userSet) {
	if labels, err := api.FetchSpaceLabels(ctx, client, sp.ID); err != nil {
		rec.fail(filepath.Join(spaceDir, "labels.json"), err)
	} else if len(labels.Space) > 0 || len(labels.Content) > 0 {
		rec.writeJSON(filepath.Join(spaceDir, "labels.json"), labels)
	}

	if tasks, err := api.FetchTasks(ctx, client, sp.ID); err != nil {
		rec.fail(filepath.Join(spaceDir, "tasks.json"), err)
	} else if len(tasks) > 0 {
		rec.writeJSON(filepath.Join(spaceDir, "tasks.json"), tasks)
	}

	if custom, err := api.FetchCustomContent(ctx, client, sp.ID); err != nil {
		rec.fail(filepath.Join(spaceDir, "custom-content.json"), err)
	} else if len(custom) > 0 {
		rec.writeJSON(filepath.Join(spaceDir, "custom-content.json"), custom)
	}

	// Whiteboards, databases, folders, embeds — discovered via CQL search
	// (v2 has no list endpoints for these), then fetched by ID. The v2 API
	// exposes metadata only; whiteboard/database contents are not exportable.
	// Folders matter for restore: pages can sit below a folder (parentType).
	for _, contentType := range api.SmartContentTypes() {
		typeDir := filepath.Join(spaceDir, contentType+"s")
		refs, missing, err := api.FetchContentIDsByType(ctx, client, sp.Key, contentType)
		if err != nil {
			rec.fail(typeDir, err)
			continue
		}
		if missing > 0 {
			rec.fail(typeDir, fmt.Errorf("search announced %d more %s(s) than it returned", missing, contentType))
		}
		for _, ref := range refs {
			item, err := api.FetchContentItem(ctx, client, contentType, ref.ID)
			name := storage.SanitizeName(ref.ID)
			if t := safeName(ref.Title, 80); t != "" {
				name += "_" + t
			}
			if err != nil {
				rec.fail(filepath.Join(typeDir, name+".json"), err)
				continue
			}
			users.addFrom(item)
			rec.write(filepath.Join(typeDir, name+".json"), indentRaw(item))
		}
	}
}

func fetchAndWriteUsers(ctx context.Context, client *api.Client, rec *recorder, accountIDs []string) {
	users := make([]json.RawMessage, 0, len(accountIDs))
	for _, id := range accountIDs {
		u, err := api.FetchUserProfile(ctx, client, id)
		if err != nil {
			rec.fail("users.json#"+id, err)
			continue
		}
		users = append(users, u)
	}
	rec.writeJSON("users.json", users)
}

// marshalJSON encodes v without HTML escaping, so the HTML in page bodies,
// comments and descriptions stays readable in the backup files.
func marshalJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
