package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/export"
	"github.com/kAYd9iN/confluence-backup/internal/model"
)

// fakeConfluence serves a small space with everything the backup must keep:
// a page tree with an archived child and a page titled ".", footer comments
// with a reply thread, an inline comment, labels, versions, an attachment,
// a blog post with its own comment and attachment, a template, and a folder.
// failPath, when set, answers 500 for that exact path.
func fakeConfluence(t *testing.T, failPath string) *httptest.Server {
	t.Helper()
	list := func(w http.ResponseWriter, items ...map[string]any) {
		if items == nil {
			items = []map[string]any{}
		}
		sendJSON(w, map[string]any{"results": items, "_links": map[string]any{}})
	}
	body := func(v string) map[string]any {
		return map[string]any{"storage": map[string]any{"value": v, "representation": "storage"}}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == failPath {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch p {
		case "/wiki/api/v2/spaces":
			list(w, map[string]any{"id": "1", "key": "KB", "name": "Knowledge", "homepageId": "10",
				"description": map[string]any{"view": map[string]any{"value": "<p>about</p>"}}, "authorId": "u-space"})
		case "/wiki/api/v2/spaces/1/permissions":
			list(w, map[string]any{"id": "perm1", "principal": map[string]any{"type": "user", "id": "u1"},
				"operation": map[string]any{"key": "read", "targetType": "space"}})
		case "/wiki/api/v2/spaces/1/pages":
			list(w,
				map[string]any{"id": "10", "title": "Home", "status": "current", "position": 0,
					"authorId": "u1", "version": map[string]any{"number": 3, "authorId": "u2"}, "body": body(homeBody)},
				map[string]any{"id": "11", "title": "Old", "status": "archived", "parentId": "10", "parentType": "page",
					"position": 1, "body": body("<p>old</p>")},
				map[string]any{"id": "12", "title": ".", "status": "current", "parentId": "10", "parentType": "page",
					"body": body("<p>dot</p>")},
			)
		case "/wiki/api/v2/pages/10/footer-comments":
			list(w, map[string]any{"id": "c1", "version": map[string]any{"authorId": "u3"}, "body": body("root")})
		case "/wiki/api/v2/footer-comments/c1/children":
			list(w, map[string]any{"id": "c2", "version": map[string]any{"authorId": "u4"}, "body": body("reply")})
		case "/wiki/api/v2/pages/10/inline-comments":
			list(w, map[string]any{"id": "i1", "resolutionStatus": "open",
				"properties": map[string]any{"inlineMarkerRef": "m1", "inlineOriginalSelection": "marked"}})
		case "/wiki/api/v2/pages/10/labels":
			list(w, map[string]any{"id": "l1", "name": "howto", "prefix": "global"})
		case "/wiki/api/v2/pages/10/versions":
			list(w, map[string]any{"number": 3}, map[string]any{"number": 2}, map[string]any{"number": 1})
		case "/wiki/api/v2/pages/10/attachments":
			list(w, map[string]any{"id": "att1", "title": "Bild.png", "mediaType": "image/png", "fileSize": 3})
		case "/wiki/rest/api/content/10/child/attachment/att1/download":
			w.Write([]byte("PNG"))
		case "/wiki/api/v2/spaces/1/blogposts":
			list(w, map[string]any{"id": "20", "title": "News", "status": "current",
				"createdAt": "2026-05-04T10:00:00Z", "body": body("<p>news</p>")})
		case "/wiki/api/v2/blogposts/20/footer-comments":
			list(w, map[string]any{"id": "c9", "body": body("nice")})
		case "/wiki/api/v2/blogposts/20/attachments":
			list(w, map[string]any{"id": "att2", "title": "a.pdf"})
		case "/wiki/rest/api/content/20/child/attachment/att2/download":
			w.Write([]byte("PDF"))
		case "/wiki/rest/api/template/page":
			sendJSON(w, map[string]any{"results": []map[string]any{{"templateId": "t1", "name": "Meeting"}}, "limit": 200})
		case "/wiki/rest/api/template/blueprint":
			sendJSON(w, map[string]any{"results": []any{}, "limit": 200})
		case "/wiki/rest/api/search":
			var results []map[string]any
			if strings.Contains(r.URL.Query().Get("cql"), "type = folder") {
				results = []map[string]any{{"content": map[string]any{"id": "f1", "type": "folder", "title": "Projects"}}}
			}
			sendJSON(w, map[string]any{"results": results, "limit": 250, "totalSize": len(results)})
		case "/wiki/api/v2/folders/f1":
			sendJSON(w, map[string]any{"id": "f1", "title": "Projects", "parentId": "10"})
		case "/wiki/rest/api/user":
			sendJSON(w, map[string]any{"accountId": r.URL.Query().Get("accountId"), "displayName": "Someone"})
		default:
			// Everything else (empty sub-collections, other comment lists, …).
			list(w)
		}
	}))
}

// sendJSON answers like Confluence: HTML in strings is not escaped.
func sendJSON(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// homeBody is the storage-format body of the fake space's homepage: links to
// a page that exists and one that does not, an embedded attachment image, a
// macro with an equivalent and one without.
const homeBody = `<p>home</p>` +
	`<p><ac:link><ri:page ri:content-title="Old"/><ac:plain-text-link-body><![CDATA[zur alten Seite]]></ac:plain-text-link-body></ac:link></p>` +
	`<ac:image ac:alt="Bild"><ri:attachment ri:filename="Bild.png"/></ac:image>` +
	`<ac:structured-macro ac:name="info"><ac:rich-text-body><p>Hinweis</p></ac:rich-text-body></ac:structured-macro>` +
	`<ac:structured-macro ac:name="children"/>` +
	`<p><ac:link><ri:page ri:content-title="Gelöscht"/></ac:link></p>`

func runBackup(t *testing.T, srv *httptest.Server) (string, error) {
	t.Helper()
	client := api.NewClient(srv.URL, "u@example.com", "tok")
	client.MaxRetries = 0
	return Run(context.Background(), client, Config{
		Domain: "example.atlassian.net", OutputDir: t.TempDir(), IncludeAttachments: true, ToolVersion: "test",
	})
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("missing %s: %v", rel, err)
	}
	return string(b)
}

func TestRun_BacksUpEverythingARestoreNeeds(t *testing.T) {
	srv := fakeConfluence(t, "")
	defer srv.Close()

	dir, err := runBackup(t, srv)
	if err != nil {
		t.Fatalf("backup reported errors: %v", err)
	}

	space := readFile(t, dir, "spaces/KB/space.json")
	for _, want := range []string{`"homepageId"`, `<p>about</p>`, `"perm1"`} {
		if !strings.Contains(space, want) {
			t.Errorf("space.json lacks %s:\n%s", want, space)
		}
	}

	if got := readFile(t, dir, "spaces/KB/pages/Home/index.html"); got != homeBody {
		t.Errorf("Home body = %q", got)
	}
	meta := readFile(t, dir, "spaces/KB/pages/Home/page.json")
	if strings.Contains(meta, "<p>home</p>") || !strings.Contains(meta, `"position"`) || !strings.Contains(meta, `"authorId"`) {
		t.Errorf("page.json must be the full object without body:\n%s", meta)
	}

	// The archived child page is backed up.
	readFile(t, dir, "spaces/KB/pages/Home/Old/index.html")
	// A page titled "." must get its own directory, not overwrite its parent.
	if got := readFile(t, dir, "spaces/KB/pages/Home/page_12/index.html"); got != "<p>dot</p>" {
		t.Errorf(`page "." body = %q`, got)
	}

	comments := readFile(t, dir, "spaces/KB/pages/Home/comments.json")
	for _, want := range []string{`"c2"`, `"replies"`, `"inlineOriginalSelection"`, `"marked"`} {
		if !strings.Contains(comments, want) {
			t.Errorf("comments.json lacks %s:\n%s", want, comments)
		}
	}
	readFile(t, dir, "spaces/KB/pages/Home/labels.json")
	readFile(t, dir, "spaces/KB/pages/Home/versions.json")
	if got := readFile(t, dir, "spaces/KB/pages/Home/attachments/files/att1_Bild.png"); got != "PNG" {
		t.Errorf("attachment content = %q", got)
	}

	// Blog posts get the same treatment as pages.
	readFile(t, dir, "spaces/KB/blog/2026-05-04_News/post.json")
	if !strings.Contains(readFile(t, dir, "spaces/KB/blog/2026-05-04_News/comments.json"), `"c9"`) {
		t.Error("blog post comment missing")
	}
	if got := readFile(t, dir, "spaces/KB/blog/2026-05-04_News/attachments/files/att2_a.pdf"); got != "PDF" {
		t.Errorf("blog attachment content = %q", got)
	}

	readFile(t, dir, "spaces/KB/templates/Meeting_t1.json")
	readFile(t, dir, "spaces/KB/folders/f1_Projects.json")

	// users.json covers page authors, version authors and commenters.
	users := readFile(t, dir, "users.json")
	for _, id := range []string{"u1", "u2", "u3", "u4", "u-space"} {
		if !strings.Contains(users, `"`+id+`"`) {
			t.Errorf("users.json lacks account %s", id)
		}
	}

	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, dir, "backup-manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if f.Status != "ok" {
			t.Errorf("unexpected manifest entry %+v", f)
		}
	}
}

// A failing sub-fetch must be visible: the old code only logged it and the
// backup still reported success.
func TestRun_FailedFetchIsRecordedAndReported(t *testing.T) {
	srv := fakeConfluence(t, "/wiki/api/v2/pages/10/labels")
	defer srv.Close()

	dir, err := runBackup(t, srv)
	if err == nil {
		t.Fatal("Run must report an error when part of the data could not be backed up")
	}
	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, dir, "backup-manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, f := range m.Files {
		if f.Status == "failed" {
			failed = append(failed, f.Name)
		}
	}
	if len(failed) != 1 || failed[0] != "spaces/KB/pages/Home/labels.json" {
		t.Errorf("failed entries = %v", failed)
	}
	// Everything else is still backed up.
	readFile(t, dir, "spaces/KB/pages/Home/index.html")
}

// The full disaster-recovery path: back up a (fake) Confluence, load the
// backup, export it as a static site — links, images and comments must work.
func TestPipeline_BackupThenExport(t *testing.T) {
	srv := fakeConfluence(t, "")
	defer srv.Close()
	dir, err := runBackup(t, srv)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	b, err := model.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Spaces) != 1 {
		t.Fatalf("spaces = %d", len(b.Spaces))
	}
	sp := b.Spaces[0]
	if len(sp.Roots) == 0 || sp.Roots[0].ID != "10" {
		t.Fatalf("homepage must lead the tree, roots = %v", sp.Roots)
	}
	// The folder from the search is part of the tree (below the homepage).
	if f := sp.ByID("f1"); f == nil || f.Kind != model.KindFolder || f.Parent == nil || f.Parent.ID != "10" {
		t.Errorf("folder f1 not placed below the homepage: %+v", f)
	}

	// CONFLUENCE_EXPORT_PREVIEW=<dir> keeps the exported site for a look in a browser.
	site := os.Getenv("CONFLUENCE_EXPORT_PREVIEW")
	if site == "" {
		site = t.TempDir()
	}
	report, err := export.Export(b, site)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if report.Pages != 3 || report.Posts != 1 || report.Attachments != 2 {
		t.Errorf("report = %+v", report)
	}

	home := readFile(t, site, "KB/p/10.html")
	for _, want := range []string{
		`<a href="../../KB/p/11.html">zur alten Seite</a>`, // page link resolved
		`<img src="../../KB/f/10/Bild.png" alt="Bild"/>`,   // embedded image resolved
		`<div class="panel panel-info"><p>Hinweis</p></div>`,
		`[Makro: children]`,
		`<span class="unresolved-link">Gelöscht</span>`,
		`Kommentare`, `root`, `reply`, // comment thread with reply
		`marked`,     // inline comment selection
		`Archiviert`, // archived child listed under Unterseiten
	} {
		if !strings.Contains(home, want) {
			t.Errorf("exported homepage lacks %q", want)
		}
	}
	if got := readFile(t, site, "KB/f/10/Bild.png"); got != "PNG" {
		t.Errorf("exported attachment = %q", got)
	}
	readFile(t, site, "KB/b/20.html")
	idx := readFile(t, site, "KB/index.html")
	if !strings.Contains(idx, "Projects") || !strings.Contains(idx, "News") {
		t.Errorf("space index lacks folder or blog post:\n%s", idx)
	}
	rep := readFile(t, site, "export-report.txt")
	if !strings.Contains(rep, "Gelöscht") || !strings.Contains(rep, "children") {
		t.Errorf("report lacks unresolved link or placeholder:\n%s", rep)
	}
}
