package backup

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/export"
	"github.com/kAYd9iN/confluence-backup/internal/model"
	"github.com/kAYd9iN/confluence-backup/internal/xwiki"
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
	`<p><ac:link><ri:page ri:content-title="Gelöscht"/></ac:link></p>` +
	`<p><ac:link><ri:attachment ri:filename="Bild.png"/><ac:plain-text-link-body><![CDATA[Bild herunterladen]]></ac:plain-text-link-body></ac:link></p>` +
	`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter>` +
	`<ac:plain-text-body><![CDATA[if a < b && c > d { return }]]></ac:plain-text-body></ac:structured-macro>` +
	`<table><tbody><tr><th>Spalte</th><th>Wert</th></tr><tr><td>Ä Ö Ü</td><td>100&nbsp;%</td></tr></tbody></table>` +
	`<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">Mehr</ac:parameter>` +
	`<ac:rich-text-body><p>Aufgeklappt</p></ac:rich-text-body></ac:structured-macro>` +
	`<ac:task-list><ac:task><ac:task-id>1</ac:task-id><ac:task-status>complete</ac:task-status>` +
	`<ac:task-body>Erledigt</ac:task-body></ac:task></ac:task-list>` +
	`<p><ac:structured-macro ac:name="status"><ac:parameter ac:name="title">FERTIG</ac:parameter>` +
	`<ac:parameter ac:name="colour">Green</ac:parameter></ac:structured-macro></p>`

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

// fakeXWiki records the REST writes of an import and answers GETs for pages
// it has received (for the post-import verification).
type fakeXWiki struct {
	mu    sync.Mutex
	puts  map[string]string // REST path (decoded) → body
	ctype map[string]string
}

func newFakeXWiki(t *testing.T) (*fakeXWiki, *httptest.Server) {
	f := &fakeXWiki{puts: map[string]string{}, ctype: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.puts[r.URL.Path] = string(body)
			f.ctype[r.URL.Path] = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if _, ok := f.puts[r.URL.Path]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeXWiki) page(t *testing.T, path string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.puts["/rest/wikis/xwiki/"+path]
	if !ok {
		var have []string
		for k := range f.puts {
			have = append(have, k)
		}
		t.Fatalf("XWiki did not receive %s; got:\n%s", path, strings.Join(have, "\n"))
	}
	return body
}

// Full recovery path into XWiki: fake Confluence → backup → load → import
// into a fake XWiki REST API. Asserts page placement, link/image rewriting,
// attachments, tags, comments and the post-import verification.
func TestPipeline_BackupThenXWikiImport(t *testing.T) {
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

	xw, xsrv := newFakeXWiki(t)
	c, err := xwiki.NewClient(xsrv.URL, "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	c.MaxRetries = 0
	rep, err := xwiki.Import(context.Background(), c, b, xwiki.Options{Verify: true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(rep.Failed) > 0 || len(rep.NotFoundAfterImport) > 0 {
		t.Fatalf("failed=%v notFound=%v", rep.Failed, rep.NotFoundAfterImport)
	}
	if rep.Spaces != 1 || rep.Pages != 3 || rep.Folders != 1 || rep.Posts != 1 || rep.Attachments != 2 {
		t.Errorf("report = %+v", rep)
	}

	// The Confluence homepage becomes the space's WebHome, titled with the space name.
	home := xw.page(t, "spaces/KB/pages/WebHome")
	for _, want := range []string{
		"<title>Knowledge</title>", "<syntax>xhtml/1.0</syntax>",
		"Aus Confluence übernommen (Bereich KB)",
		`href=&#34;/bin/view/KB/Old/&#34;`,              // page link rewritten to XWiki
		`src=&#34;Bild\.png&#34;`, // embedded image → XWiki attachment reference (escaped)
		`href=&#34;/bin/download/KB/WebHome/Bild.png&#34;`, // attachment link → download URL
		`class=&#34;box infomessage&#34;`,                 // info panel → XWiki message box
		"Kommentare aus Confluence", "root", "reply", "marked",
	} {
		if !strings.Contains(home, want) {
			t.Errorf("homepage lacks %q:\n%s", want, home)
		}
	}
	// Children of the homepage sit below the space; "." gets a safe name; the
	// archived page is flagged; the folder is a page of its own.
	if old := xw.page(t, "spaces/KB/spaces/Old/pages/WebHome"); !strings.Contains(old, "archiviert") {
		t.Errorf("archived page not flagged:\n%s", old)
	}
	xw.page(t, "spaces/KB/spaces/Seite-12/pages/WebHome")
	xw.page(t, "spaces/KB/spaces/Projects/pages/WebHome")
	// Blog posts under KB/Blog.
	xw.page(t, "spaces/KB/spaces/Blog/pages/WebHome")
	xw.page(t, "spaces/KB/spaces/Blog/spaces/2026-05-04 News/pages/WebHome")

	if got := xw.page(t, "spaces/KB/pages/WebHome/attachments/Bild.png"); got != "PNG" {
		t.Errorf("attachment body = %q", got)
	}
	if ct := xw.ctype["/rest/wikis/xwiki/spaces/KB/pages/WebHome/attachments/Bild.png"]; ct != "image/png" {
		t.Errorf("attachment content type = %q", ct)
	}
	tags := xw.page(t, "spaces/KB/pages/WebHome/tags")
	if !strings.Contains(tags, `name="howto"`) || !strings.Contains(tags, `name="confluence"`) {
		t.Errorf("tags = %s", tags)
	}
}

// Wrong XWiki credentials abort the import at the first request instead of
// failing every page one by one.
func TestXWikiImport_AbortsOnAuthFailure(t *testing.T) {
	srv := fakeConfluence(t, "")
	defer srv.Close()
	dir, err := runBackup(t, srv)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := model.Load(dir)
	_, xsrv := newFakeXWiki(t)
	c, _ := xwiki.NewClient(xsrv.URL, "admin", "wrong")
	c.MaxRetries = 0
	if _, err := xwiki.Import(context.Background(), c, b, xwiki.Options{}); err == nil {
		t.Fatal("expected the import to abort on HTTP 401")
	}
}

// TestXWikiImport_Live imports the fake backup into a REAL XWiki and checks the
// result through XWiki's own REST API and renderer. Opt-in: set XWIKI_TEST_URL
// (e.g. http://127.0.0.1:18080), XWIKI_USER and XWIKI_PASSWORD. Pages go below
// the parent page "CBImportTest".
func TestXWikiImport_Live(t *testing.T) {
	base := os.Getenv("XWIKI_TEST_URL")
	if base == "" {
		t.Skip("set XWIKI_TEST_URL, XWIKI_USER and XWIKI_PASSWORD to run against a real XWiki")
	}
	user, pass := os.Getenv("XWIKI_USER"), os.Getenv("XWIKI_PASSWORD")

	srv := fakeConfluence(t, "")
	defer srv.Close()
	dir, err := runBackup(t, srv)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	b, err := model.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := xwiki.NewClient(base, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	// Import twice: a re-run must overwrite, not duplicate or fail.
	for run := 1; run <= 2; run++ {
		rep, err := xwiki.Import(context.Background(), c, b, xwiki.Options{Parent: []string{"CBImportTest"}, Verify: true})
		if err != nil {
			t.Fatalf("import run %d: %v", run, err)
		}
		if len(rep.Failed) > 0 || len(rep.NotFoundAfterImport) > 0 {
			t.Fatalf("import run %d: failed=%v notFound=%v", run, rep.Failed, rep.NotFoundAfterImport)
		}
		t.Logf("import run %d: %+v", run, rep)
	}

	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+path, nil)
		req.SetBasicAuth(user, pass)
		req.Header.Set("Accept", "application/xml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Stored page: title, syntax and content as sent.
	code, page := get("/rest/wikis/xwiki/spaces/CBImportTest/spaces/KB/pages/WebHome")
	if code != 200 || !strings.Contains(page, "<syntax>xhtml/1.0</syntax>") || !strings.Contains(page, "<title>Knowledge</title>") {
		t.Fatalf("stored homepage (HTTP %d):\n%.2000s", code, page)
	}
	// Attachment content round-trips.
	if code, att := get("/rest/wikis/xwiki/spaces/CBImportTest/spaces/KB/pages/WebHome/attachments/Bild.png"); code != 200 || att != "PNG" {
		t.Errorf("attachment: HTTP %d %q", code, att)
	}
	// Tags.
	if _, tags := get("/rest/wikis/xwiki/spaces/CBImportTest/spaces/KB/pages/WebHome/tags"); !strings.Contains(tags, "howto") {
		t.Errorf("tags: %s", tags)
	}
	// Rendered by XWiki (content only, no skin): the xhtml/1.0 content must
	// actually render, with rewritten links and images.
	code, html := get("/bin/get/CBImportTest/KB/WebHome?xpage=plain&outputSyntax=html")
	if code != 200 {
		t.Fatalf("render: HTTP %d\n%.1500s", code, html)
	}
	for _, want := range []string{"Hinweis", "Aufgeklappt", "FERTIG", "Erledigt", "Ä Ö Ü",
		"if a &lt; b &amp;&amp; c &gt; d", "/CBImportTest/KB/Old/", "Kommentare aus Confluence",
		`src="/bin/download/CBImportTest/KB/WebHome/Bild.png`,  // image rendered from the attachment reference
		`href="/bin/download/CBImportTest/KB/WebHome/Bild.png`, // attachment link
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page lacks %q", want)
		}
	}
	if t.Failed() {
		t.Logf("rendered page:\n%.4000s", html)
	}
}
