package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sync"
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

// FormatBackupDir returns the timestamp-based directory name for a backup,
// using the local timezone so the date matches what the operator sees on their system.
func FormatBackupDir(t time.Time) string {
	return t.Local().Format("2006-01-02T15-04-05")
}

// Run executes the full backup and returns the path to the created backup directory.
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

	// --- Collect all user account IDs ---
	var userMu sync.Mutex
	accountIDs := make(map[string]struct{})
	collectUser := func(id string) {
		if id == "" {
			return
		}
		userMu.Lock()
		accountIDs[id] = struct{}{}
		userMu.Unlock()
	}

	// --- Process spaces (3 concurrent) ---
	spaceSem := make(chan struct{}, 3)
	pageSem := make(chan struct{}, 20) // global cap across all spaces

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	addErr := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	for _, sp := range active {
		sp := sp
		wg.Add(1)
		go func() {
			defer wg.Done()
			spaceSem <- struct{}{}
			defer func() { <-spaceSem }()

			if err := processSpace(ctx, client, w, manifest, sp, cfg,
				pageSem, collectUser); err != nil {
				addErr(fmt.Errorf("space %s: %w", sp.Key, err))
				slog.Error("space backup failed", "key", sp.Key, "err", err)
			}
		}()
	}
	wg.Wait()

	// --- Fetch user profiles ---
	if err := fetchAndWriteUsers(ctx, client, w, manifest, accountIDs); err != nil {
		slog.Warn("user profiles partially failed", "err", err)
	}

	// --- Write unsigned manifest (signing happens in main after Run returns) ---
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	w.WriteFile("backup-manifest.json", manifestData) // #nosec G104 -- error logged or non-critical in backup context

	if len(errs) > 0 {
		return w.Dir(), fmt.Errorf("%d space(s) had errors: first: %w", len(errs), errs[0])
	}
	return w.Dir(), nil
}

func processSpace(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, sp api.Space, cfg Config,
	pageSem chan struct{}, collectUser func(string)) error {

	slog.Info("backing up space", "key", sanitizeLog(sp.Key), "name", sanitizeLog(sp.Name))

	// Space detail (permissions + properties)
	detail, err := api.FetchSpaceDetail(ctx, client, sp)
	if err != nil {
		return err
	}
	spaceJSON, _ := json.MarshalIndent(detail, "", "  ")
	relPath := filepath.Join("spaces", sp.Key, "space.json")
	if !cfg.DryRun {
		if err := w.WriteFile(relPath, spaceJSON); err != nil {
			return err
		}
		if err := manifest.AddFile(filepath.Join(w.Dir(), relPath)); err != nil {
			slog.Warn("manifest update failed", "path", relPath, "err", err)
		}
	}

	// Templates
	templates, err := api.FetchTemplates(ctx, client, sp.Key)
	if err != nil {
		slog.Warn("templates fetch failed", "space", sp.Key, "err", err)
	}
	for _, tmpl := range templates {
		tJSON, err := json.MarshalIndent(tmpl, "", "  ")
		if err != nil {
			slog.Warn("template marshal failed", "space", sp.Key, "name", tmpl.Name, "err", err)
			continue
		}
		tPath := filepath.Join("spaces", sp.Key, "templates",
			storage.SanitizeName(tmpl.Name)+".json")
		if !cfg.DryRun {
			if err := w.WriteFile(tPath, tJSON); err != nil {
				slog.Warn("template write failed", "space", sp.Key, "name", tmpl.Name, "err", err)
			} else if err := manifest.AddFile(filepath.Join(w.Dir(), tPath)); err != nil {
				slog.Warn("manifest update failed", "path", tPath, "err", err)
			}
		}
	}

	// Pages (flat fetch → build tree → write recursively)
	pages, err := api.FetchPages(ctx, client, sp.ID)
	if err != nil {
		return fmt.Errorf("fetch pages: %w", err)
	}
	for _, p := range pages {
		collectUser(p.Version.AuthorID)
	}

	roots := BuildTree(pages)
	var pageWg sync.WaitGroup
	var pageErrs []error
	var pageErrMu sync.Mutex

	var writeTree func(nodes []*PageNode, parentRelPath string, depth int)
	writeTree = func(nodes []*PageNode, parentRelPath string, depth int) {
		if depth > maxTreeDepth {
			slog.Warn("page tree depth limit reached — skipping subtree", "depth", depth, "limit", maxTreeDepth)
			return
		}
		for _, node := range nodes {
			node := node
			pageWg.Add(1)
			go func() {
				defer pageWg.Done()
				pageSem <- struct{}{}
				defer func() { <-pageSem }()

				dirPath := filepath.Join(parentRelPath, node.DirName())
				if err := writePage(ctx, client, w, manifest, node.Page,
					dirPath, cfg); err != nil {
					pageErrMu.Lock()
					pageErrs = append(pageErrs, err)
					pageErrMu.Unlock()
					manifest.AddFailedFile(dirPath+"/page.json", err)
					slog.Error("page backup failed",
						"id", node.Page.ID,
						"title", sanitizeLog(node.Page.Title),
						"err", err)
				}
				writeTree(node.Children, dirPath, depth+1)
			}()
		}
	}
	writeTree(roots, filepath.Join("spaces", sp.Key, "pages"), 0)
	pageWg.Wait()

	// Blog posts
	posts, err := api.FetchBlogPosts(ctx, client, sp.ID)
	if err != nil {
		slog.Warn("blog posts fetch failed", "space", sp.Key, "err", err)
	}
	for _, post := range posts {
		post := post
		dirName := post.Version.CreatedAt[:10] + "_" + storage.SanitizeName(post.Title)
		dirPath := filepath.Join("spaces", sp.Key, "blog", dirName)
		if !cfg.DryRun {
			writePost(ctx, client, w, manifest, post, dirPath)
		}
	}

	if len(pageErrs) > 0 {
		return fmt.Errorf("%d page(s) failed", len(pageErrs))
	}
	return nil
}

func writePage(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, page api.Page, dirPath string, cfg Config) error {

	// index.html — failure is fatal for this page
	htmlPath := filepath.Join(dirPath, "index.html")
	if !cfg.DryRun {
		if err := w.WriteFile(htmlPath, []byte(page.Body.Storage.Value)); err != nil {
			return err
		}
		if err := manifest.AddFile(filepath.Join(w.Dir(), htmlPath)); err != nil {
			slog.Warn("manifest update failed", "path", htmlPath, "err", err)
		}
	}

	// page.json (metadata without body) — failure is fatal for this page
	meta := struct {
		ID       string          `json:"id"`
		Title    string          `json:"title"`
		SpaceID  string          `json:"spaceId"`
		ParentID string          `json:"parentId,omitempty"`
		Status   string          `json:"status"`
		Version  api.PageVersion `json:"version"`
		Labels   []api.Label     `json:"labels,omitempty"`
	}{
		ID: page.ID, Title: page.Title, SpaceID: page.SpaceID,
		ParentID: page.ParentID, Status: page.Status,
		Version: page.Version, Labels: page.Labels,
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal page metadata: %w", err)
	}
	metaPath := filepath.Join(dirPath, "page.json")
	if !cfg.DryRun {
		if err := w.WriteFile(metaPath, metaJSON); err != nil {
			return err
		}
		if err := manifest.AddFile(filepath.Join(w.Dir(), metaPath)); err != nil {
			slog.Warn("manifest update failed", "path", metaPath, "err", err)
		}
	}

	// comments.json — non-critical, log and continue on failure
	comments, err := api.FetchComments(ctx, client, page.ID)
	if err != nil {
		slog.Warn("comments fetch failed", "pageId", page.ID, "err", err)
	} else if !cfg.DryRun {
		cJSON, err := json.MarshalIndent(comments, "", "  ")
		if err != nil {
			slog.Warn("comments marshal failed", "pageId", page.ID, "err", err)
		} else {
			cPath := filepath.Join(dirPath, "comments.json")
			if err := w.WriteFile(cPath, cJSON); err != nil {
				slog.Warn("comments write failed", "pageId", page.ID, "err", err)
			} else if err := manifest.AddFile(filepath.Join(w.Dir(), cPath)); err != nil {
				slog.Warn("manifest update failed", "path", cPath, "err", err)
			}
		}
	}

	// attachments — non-critical, log and continue on failure
	atts, err := api.FetchAttachmentMeta(ctx, client, page.ID)
	if err != nil {
		slog.Warn("attachments fetch failed", "pageId", page.ID, "err", err)
	} else if !cfg.DryRun {
		attJSON, err := json.MarshalIndent(atts, "", "  ")
		if err != nil {
			slog.Warn("attachments marshal failed", "pageId", page.ID, "err", err)
		} else {
			attPath := filepath.Join(dirPath, "attachments", "metadata.json")
			if err := w.WriteFile(attPath, attJSON); err != nil {
				slog.Warn("attachments metadata write failed", "pageId", page.ID, "err", err)
			} else if err := manifest.AddFile(filepath.Join(w.Dir(), attPath)); err != nil {
				slog.Warn("manifest update failed", "path", attPath, "err", err)
			}
		}
		if cfg.IncludeAttachments {
			downloadAttachments(ctx, client, w, manifest, atts, dirPath)
		}
	}

	return nil
}

func writePost(_ context.Context, _ *api.Client, w *storage.Writer,
	manifest *Manifest, post api.BlogPost, dirPath string) {
	htmlPath := filepath.Join(dirPath, "index.html")
	if err := w.WriteFile(htmlPath, []byte(post.Body.Storage.Value)); err != nil {
		slog.Warn("blog post html write failed", "postId", post.ID, "err", err)
	} else if err := manifest.AddFile(filepath.Join(w.Dir(), htmlPath)); err != nil {
		slog.Warn("manifest update failed", "path", htmlPath, "err", err)
	}

	postJSON, err := json.MarshalIndent(post, "", "  ")
	if err != nil {
		slog.Warn("blog post marshal failed", "postId", post.ID, "err", err)
		return
	}
	jsonPath := filepath.Join(dirPath, "post.json")
	if err := w.WriteFile(jsonPath, postJSON); err != nil {
		slog.Warn("blog post json write failed", "postId", post.ID, "err", err)
	} else if err := manifest.AddFile(filepath.Join(w.Dir(), jsonPath)); err != nil {
		slog.Warn("manifest update failed", "path", jsonPath, "err", err)
	}
}

func downloadAttachments(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, atts []api.Attachment, dirPath string) {
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, att := range atts {
		att := att
		if att.Links.Download == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			downloadURL := client.BaseURL() + att.Links.Download
			rc, err := client.Download(ctx, downloadURL)
			if err != nil {
				slog.Warn("attachment download failed", "name", att.Title, "err", err)
				return
			}
			defer rc.Close()
			filePath := filepath.Join(dirPath, "attachments", "files",
				storage.SanitizeName(att.Title))
			if err := w.WriteBinaryStream(filePath, rc); err != nil {
				slog.Warn("attachment write failed", "name", att.Title, "err", err)
				return
			}
			if err := manifest.AddFile(filepath.Join(w.Dir(), filePath)); err != nil {
				slog.Warn("manifest update failed", "path", filePath, "err", err)
			}
		}()
	}
	wg.Wait()
}

func fetchAndWriteUsers(ctx context.Context, client *api.Client, w *storage.Writer,
	manifest *Manifest, accountIDs map[string]struct{}) error {
	users := make([]api.User, 0, len(accountIDs))
	for id := range accountIDs {
		u, err := api.FetchUserProfile(ctx, client, id)
		if err != nil {
			slog.Warn("user profile fetch failed", "accountId", id, "err", err)
			continue
		}
		users = append(users, u)
	}
	usersJSON, _ := json.MarshalIndent(users, "", "  ")
	usersPath := "users.json"
	if err := w.WriteFile(usersPath, usersJSON); err != nil {
		return err
	}
	if err := manifest.AddFile(filepath.Join(w.Dir(), usersPath)); err != nil {
		slog.Warn("manifest update failed", "path", usersPath, "err", err)
	}
	return nil
}
