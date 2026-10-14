// Package export writes a loaded backup as a static HTML site: readable in
// any browser without a server or a wiki, so the content stays accessible
// while Confluence is unavailable.
//
// Layout:
//
//	index.html                  all spaces
//	style.css
//	export-report.txt           unresolved links and macro placeholders
//	<KEY>/index.html            page tree and blog posts of a space
//	<KEY>/p/<pageId>.html       pages
//	<KEY>/b/<postId>.html       blog posts
//	<KEY>/f/<contentId>/<file>  attachments
package export

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kAYd9iN/confluence-backup/internal/convert"
	"github.com/kAYd9iN/confluence-backup/internal/model"
	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// Report summarizes what the export could not represent faithfully.
type Report struct {
	Pages, Posts, Attachments int
	MissingFiles              int            // attachments whose file is not in the backup
	Placeholders              map[string]int // macro → count
	Unresolved                map[string][]string
	ParseErrors               map[string]string
}

type exporter struct {
	b       *model.Backup
	out     string
	stamp   string
	report  Report
	titles  map[string]map[string]*model.Page // space key → title → page/folder
	posts   map[string]map[string]*model.Page // space key → title → blog post
	ids     map[string]*model.Page            // content ID → page/folder/post
	fileOut map[*model.Page]map[string]string // page → attachment title → exported file name
}

// Export writes the site for backup b into outDir (created if needed).
func Export(b *model.Backup, outDir string) (Report, error) {
	e := &exporter{b: b, out: outDir, report: Report{
		Placeholders: map[string]int{}, Unresolved: map[string][]string{}, ParseErrors: map[string]string{},
	}, titles: map[string]map[string]*model.Page{}, posts: map[string]map[string]*model.Page{},
		ids: map[string]*model.Page{}, fileOut: map[*model.Page]map[string]string{}}
	e.stamp = backupTimestamp(b.Dir)

	for _, s := range b.Spaces {
		e.titles[s.Key] = map[string]*model.Page{}
		e.posts[s.Key] = map[string]*model.Page{}
		for _, p := range s.Pages {
			e.titles[s.Key][p.Title] = p
			e.ids[p.ID] = p
		}
		for _, p := range s.Posts {
			e.posts[s.Key][p.Title] = p
			e.ids[p.ID] = p
		}
	}

	if err := e.write("style.css", []byte(styleCSS)); err != nil {
		return e.report, err
	}
	for _, s := range b.Spaces {
		if err := e.exportSpace(s); err != nil {
			return e.report, err
		}
	}
	if err := e.write("index.html", []byte(e.rootIndex())); err != nil {
		return e.report, err
	}
	return e.report, e.write("export-report.txt", []byte(e.reportText()))
}

func backupTimestamp(dir string) string {
	var m struct {
		Timestamp string `json:"timestamp"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "backup-manifest.json")) // #nosec G304
	if err == nil && json.Unmarshal(data, &m) == nil && m.Timestamp != "" {
		return m.Timestamp
	}
	return "unbekannt"
}

func (e *exporter) write(rel string, data []byte) error {
	path := filepath.Join(e.out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func key(s *model.Space) string { return storage.SanitizeName(s.Key) }

// pageHref is the path of a page/post/folder relative to the site root.
func pageHref(p *model.Page) string {
	switch p.Kind {
	case model.KindBlogPost:
		return key(p.Space) + "/b/" + storage.SanitizeName(p.ID) + ".html"
	case model.KindFolder:
		return key(p.Space) + "/index.html#folder-" + storage.SanitizeName(p.ID)
	}
	return key(p.Space) + "/p/" + storage.SanitizeName(p.ID) + ".html"
}

// --- spaces ---

func (e *exporter) exportSpace(s *model.Space) error {
	// Attachments first, so links resolve to files that exist.
	for _, p := range append(append([]*model.Page{}, s.Pages...), s.Posts...) {
		if err := e.copyAttachments(p); err != nil {
			return err
		}
	}
	for _, p := range s.Pages {
		if p.Kind != model.KindPage {
			continue
		}
		if err := e.exportPage(p); err != nil {
			return err
		}
		e.report.Pages++
	}
	for _, p := range s.Posts {
		if err := e.exportPage(p); err != nil {
			return err
		}
		e.report.Posts++
	}
	return e.write(key(s)+"/index.html", []byte(e.spaceIndex(s)))
}

func (e *exporter) copyAttachments(p *model.Page) error {
	names := map[string]string{}
	used := map[string]bool{}
	for _, a := range p.Attachments {
		if a.Path == "" {
			e.report.MissingFiles++
			continue
		}
		name := storage.SanitizeName(a.Title)
		if name == "" || used[name] {
			name = storage.SanitizeName(a.ID) + "_" + name
		}
		used[name] = true
		names[a.Title] = name
		if err := copyFile(a.Path, filepath.Join(e.out, key(p.Space), "f", storage.SanitizeName(p.ID), name)); err != nil {
			return fmt.Errorf("copy attachment %s of %s: %w", a.Title, p.ID, err)
		}
		e.report.Attachments++
	}
	e.fileOut[p] = names
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- file inside the backup being exported
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) // #nosec G304
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close() // #nosec G104 -- the copy error is the one worth reporting
		return err
	}
	return out.Close()
}

// --- resolver for one page ---

type resolver struct {
	e    *exporter
	page *model.Page
}

const up = "../../" // every page file lives two levels below the site root

func (r resolver) space(k string) string {
	if k == "" {
		return r.page.Space.Key
	}
	return k
}

func (r resolver) Page(spaceKey, title string) (string, bool) {
	k := r.space(spaceKey)
	if title == "" { // link to a space
		for _, s := range r.e.b.Spaces {
			if s.Key == k {
				return up + key(s) + "/index.html", true
			}
		}
		return "", false
	}
	if p := r.e.titles[k][title]; p != nil {
		return up + pageHref(p), true
	}
	return "", false
}

func (r resolver) BlogPost(spaceKey, title, _ string) (string, bool) {
	if p := r.e.posts[r.space(spaceKey)][title]; p != nil {
		return up + pageHref(p), true
	}
	return "", false
}

func (r resolver) Content(id string) (string, bool) {
	if p := r.e.ids[id]; p != nil {
		return up + pageHref(p), true
	}
	return "", false
}

func (r resolver) Attachment(spaceKey, pageTitle, filename string) (string, bool) {
	p := r.page
	if pageTitle != "" {
		k := r.space(spaceKey)
		if p = r.e.titles[k][pageTitle]; p == nil {
			if p = r.e.posts[k][pageTitle]; p == nil {
				return "", false
			}
		}
	}
	name, ok := r.e.fileOut[p][filename]
	if !ok {
		return "", false
	}
	return up + key(p.Space) + "/f/" + storage.SanitizeName(p.ID) + "/" + name, true
}

func (r resolver) Image(spaceKey, pageTitle, filename string) (string, bool) {
	return r.Attachment(spaceKey, pageTitle, filename)
}

func (r resolver) User(id string) string { return r.e.b.Users[id] }

// --- page rendering ---

func (e *exporter) convert(p *model.Page, storageHTML, what string) string {
	res, err := convert.Convert(storageHTML, resolver{e: e, page: p})
	label := p.Space.Key + ": " + p.Title
	if err != nil {
		e.report.ParseErrors[label+what] = err.Error()
	}
	for m, n := range res.Placeholders {
		e.report.Placeholders[m] += n
	}
	if len(res.Unresolved) > 0 {
		e.report.Unresolved[label] = append(e.report.Unresolved[label], res.Unresolved...)
	}
	return res.HTML
}

func (e *exporter) exportPage(p *model.Page) error {
	body, err := p.Body()
	if err != nil {
		return fmt.Errorf("read body of %s: %w", p.ID, err)
	}
	comments, err := p.Comments()
	if err != nil {
		return err
	}
	var b strings.Builder
	esc := html.EscapeString
	b.WriteString(pageHead(p.Title+" – "+p.Space.Name, up))
	b.WriteString(`<nav class="crumbs"><a href="` + up + `index.html">Alle Bereiche</a> › <a href="` + up + key(p.Space) + `/index.html">` + esc(p.Space.Name) + `</a>`)
	var chain []*model.Page
	for a := p.Parent; a != nil; a = a.Parent {
		chain = append([]*model.Page{a}, chain...)
	}
	for _, a := range chain {
		b.WriteString(` › <a href="` + up + pageHref(a) + `">` + esc(a.Title) + `</a>`)
	}
	b.WriteString("</nav>\n<article>\n<h1>" + esc(p.Title) + "</h1>\n")
	b.WriteString(`<p class="meta">`)
	if p.Status == "archived" {
		b.WriteString(`<span class="badge">Archiviert</span> `)
	}
	b.WriteString("Erstellt von " + esc(e.user(p.AuthorID)) + " am " + esc(day(p.CreatedAt)))
	if p.VersionNumber > 0 {
		b.WriteString(fmt.Sprintf(" · Version %d, zuletzt geändert von %s am %s",
			p.VersionNumber, esc(e.user(p.VersionAuthorID)), esc(day(p.VersionCreatedAt))))
	}
	b.WriteString("</p>\n")
	if len(p.Labels) > 0 {
		b.WriteString(`<p class="labels">`)
		for _, l := range p.Labels {
			b.WriteString(`<span class="label">` + esc(l) + `</span> `)
		}
		b.WriteString("</p>\n")
	}
	b.WriteString(`<div class="content">` + e.convert(p, body, "") + "</div>\n")

	if len(p.Children) > 0 {
		b.WriteString("<section><h2>Unterseiten</h2>\n" + e.tree(p.Children, up) + "</section>\n")
	}
	if len(p.Attachments) > 0 {
		b.WriteString("<section><h2>Anhänge</h2><ul>\n")
		r := resolver{e: e, page: p}
		for _, a := range p.Attachments {
			if href, ok := r.Attachment("", "", a.Title); ok {
				b.WriteString(`<li><a href="` + esc(href) + `">` + esc(a.Title) + "</a> " + size(a.FileSize) + "</li>\n")
			} else {
				b.WriteString("<li>" + esc(a.Title) + " <em>(Datei nicht im Backup)</em></li>\n")
			}
		}
		b.WriteString("</ul></section>\n")
	}
	if len(comments) > 0 {
		b.WriteString("<section class=\"comments\"><h2>Kommentare</h2>\n")
		for _, c := range comments {
			b.WriteString(e.comment(p, c))
		}
		b.WriteString("</section>\n")
	}
	b.WriteString("</article>\n" + e.footer() + "</body></html>\n")
	return e.write(pageHref(p), []byte(b.String()))
}

func (e *exporter) comment(p *model.Page, c model.Comment) string {
	esc := html.EscapeString
	var b strings.Builder
	class := "comment"
	if c.Inline {
		class += " inline"
	}
	b.WriteString(`<div class="` + class + `"><p class="comment-meta">` + esc(e.user(c.AuthorID)) + " · " + esc(day(c.CreatedAt)))
	if c.Inline {
		b.WriteString(" · Inline-Kommentar")
		if c.Resolution != "" {
			b.WriteString(" (" + esc(c.Resolution) + ")")
		}
	}
	b.WriteString("</p>")
	if c.Selection != "" {
		b.WriteString("<blockquote>" + esc(c.Selection) + "</blockquote>")
	}
	b.WriteString(e.convert(p, c.Body, " (Kommentar "+c.ID+")"))
	for _, r := range c.Replies {
		b.WriteString(e.comment(p, r))
	}
	b.WriteString("</div>\n")
	return b.String()
}

func (e *exporter) user(id string) string {
	if n := e.b.Users[id]; n != "" {
		return n
	}
	if id == "" {
		return "unbekannt"
	}
	return "Konto " + id
}

func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	if ts == "" {
		return "?"
	}
	return ts
}

func size(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1024:
		return fmt.Sprintf("(%d B)", n)
	case n < 1024*1024:
		return fmt.Sprintf("(%.0f KB)", float64(n)/1024)
	}
	return fmt.Sprintf("(%.1f MB)", float64(n)/1024/1024)
}

// tree renders pages and folders as a nested list; prefix leads to the site root.
func (e *exporter) tree(ps []*model.Page, prefix string) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString("<ul class=\"tree\">\n")
	for _, p := range ps {
		b.WriteString("<li>")
		if p.Kind == model.KindFolder {
			b.WriteString(`<span class="folder" id="folder-` + esc(storage.SanitizeName(p.ID)) + `">📁 ` + esc(p.Title) + "</span>")
		} else {
			b.WriteString(`<a href="` + esc(prefix+pageHref(p)) + `">` + esc(p.Title) + "</a>")
			if p.Status == "archived" {
				b.WriteString(` <span class="badge">Archiviert</span>`)
			}
		}
		if len(p.Children) > 0 {
			b.WriteString(e.tree(p.Children, prefix))
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</ul>\n")
	return b.String()
}

func (e *exporter) spaceIndex(s *model.Space) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(pageHead(s.Name, "../"))
	b.WriteString(`<nav class="crumbs"><a href="../index.html">Alle Bereiche</a></nav>` + "\n<article>\n")
	b.WriteString("<h1>" + esc(s.Name) + ` <span class="key">` + esc(s.Key) + "</span></h1>\n")
	if s.Description != "" {
		b.WriteString("<p>" + esc(s.Description) + "</p>\n")
	}
	b.WriteString("<h2>Seiten</h2>\n" + e.tree(s.Roots, "../"))
	if len(s.Posts) > 0 {
		b.WriteString("<h2>Blog</h2><ul>\n")
		for _, p := range s.Posts {
			b.WriteString(`<li>` + esc(day(p.CreatedAt)) + ` – <a href="../` + esc(pageHref(p)) + `">` + esc(p.Title) + "</a></li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</article>\n" + e.footer() + "</body></html>\n")
	return b.String()
}

func (e *exporter) rootIndex() string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(pageHead("Confluence-Backup", ""))
	b.WriteString("<article>\n<h1>Confluence-Backup</h1>\n<p>Stand des Backups: " + esc(e.stamp) + "</p>\n<ul>\n")
	for _, s := range e.b.Spaces {
		b.WriteString(`<li><a href="` + esc(key(s)) + `/index.html">` + esc(s.Name) + `</a> <span class="key">` + esc(s.Key) + "</span></li>\n")
	}
	b.WriteString("</ul>\n</article>\n" + e.footer() + "</body></html>\n")
	return b.String()
}

func (e *exporter) footer() string {
	return `<footer>Statischer Export aus confluence-backup · Stand des Backups: ` + html.EscapeString(e.stamp) + "</footer>\n"
}

func pageHead(title, prefix string) string {
	return "<!DOCTYPE html>\n<html lang=\"de\"><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
		"<title>" + html.EscapeString(title) + "</title>" +
		"<link rel=\"stylesheet\" href=\"" + prefix + "style.css\"></head><body>\n"
}

func (e *exporter) reportText() string {
	var b strings.Builder
	r := e.report
	fmt.Fprintf(&b, "Export-Bericht (Backup-Stand %s)\n\n", e.stamp)
	fmt.Fprintf(&b, "Seiten: %d, Blogposts: %d, Anhänge kopiert: %d, Anhänge ohne Datei im Backup: %d\n\n",
		r.Pages, r.Posts, r.Attachments, r.MissingFiles)

	b.WriteString("Makros ohne Entsprechung (als Platzhalter dargestellt, Inhalt erhalten):\n")
	if len(r.Placeholders) == 0 {
		b.WriteString("  keine\n")
	}
	for _, m := range sortedKeys(r.Placeholders) {
		fmt.Fprintf(&b, "  %-30s %d\n", m, r.Placeholders[m])
	}

	b.WriteString("\nNicht auflösbare Verweise (Ziel nicht im Backup, z. B. gelöscht oder in ausgeschlossenem Bereich):\n")
	if len(r.Unresolved) == 0 {
		b.WriteString("  keine\n")
	}
	for _, page := range sortedKeys(r.Unresolved) {
		fmt.Fprintf(&b, "  %s\n", page)
		for _, u := range r.Unresolved[page] {
			fmt.Fprintf(&b, "    - %s\n", u)
		}
	}
	if len(r.ParseErrors) > 0 {
		b.WriteString("\nNur teilweise lesbare Inhalte:\n")
		for _, k := range sortedKeys(r.ParseErrors) {
			fmt.Fprintf(&b, "  %s: %s\n", k, r.ParseErrors[k])
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const styleCSS = `body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:60rem;margin:0 auto;padding:1rem 1.25rem;line-height:1.55;color:#172b4d}
a{color:#0052cc}h1 .key,.key{font-size:.6em;color:#6b778c;font-weight:normal}
.crumbs{font-size:.9rem;color:#6b778c;margin-bottom:1rem}.meta,.comment-meta{color:#6b778c;font-size:.9rem}
.badge{background:#ffab00;color:#172b4d;border-radius:3px;padding:0 .4em;font-size:.8rem}
.label{background:#dfe1e6;border-radius:3px;padding:0 .4em;font-size:.85rem}
table{border-collapse:collapse;margin:.5rem 0}td,th{border:1px solid #c1c7d0;padding:.3rem .5rem;vertical-align:top}th{background:#f4f5f7}
pre{background:#f4f5f7;padding:.75rem;overflow-x:auto}code{font-family:ui-monospace,Consolas,monospace}
img{max-width:100%}blockquote{border-left:3px solid #c1c7d0;margin-left:0;padding-left:1rem;color:#42526e}
.panel{border-left:4px solid #0065ff;background:#deebff;padding:.5rem 1rem;margin:.75rem 0}
.panel-warning,.panel-error{border-color:#ff5630;background:#ffebe6}.panel-note{border-color:#ffab00;background:#fffae6}
.panel-tip,.panel-success{border-color:#36b37e;background:#e3fcef}.panel-title{font-weight:600;margin:0}
.status{border-radius:3px;padding:0 .4em;font-size:.8rem;font-weight:600;background:#dfe1e6}
.status-green{background:#e3fcef}.status-red{background:#ffebe6}.status-yellow{background:#fffae6}.status-blue{background:#deebff}
.macro-placeholder{color:#6b778c;font-style:italic}.unresolved-link,.unresolved-image{color:#bf2600;text-decoration:underline dotted}
.task-list{list-style:none;padding-left:0}.layout-section{display:flex;gap:1rem;flex-wrap:wrap}.layout-cell{flex:1;min-width:15rem}
.comments .comment{border-left:2px solid #dfe1e6;padding-left:.75rem;margin:.75rem 0}.comment.inline{border-color:#ffab00}
.tree{padding-left:1.25rem}.folder{color:#42526e}
footer{margin-top:3rem;padding-top:.75rem;border-top:1px solid #dfe1e6;color:#6b778c;font-size:.85rem}
`
