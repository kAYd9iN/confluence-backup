package xwiki

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kAYd9iN/confluence-backup/internal/convert"
	"github.com/kAYd9iN/confluence-backup/internal/model"
)

// Options controls an import.
type Options struct {
	Wiki   string   // XWiki wiki ID, default "xwiki"
	Parent []string // optional page path to import below, e.g. ["Confluence"]
	DryRun bool     // plan only, no requests
	Verify bool     // GET every imported page afterwards
	// Progress, if set, receives status messages.
	Progress func(msg string, args ...any)
}

// Report summarizes an import.
type Report struct {
	Spaces, Pages, Folders, Posts, Attachments int
	MissingFiles                               int // attachments without a file in the backup
	Failed                                     []string
	NotFoundAfterImport                        []string
	Placeholders                               map[string]int
	Unresolved                                 map[string][]string
	Plan                                       []string // dry run: planned page locations
}

type importer struct {
	c        *Client
	o        Options
	b        *model.Backup
	loc      map[*model.Page][]string // page → XWiki space path (the page is <path>/WebHome)
	spaceLoc map[*model.Space][]string
	home     map[*model.Space]*model.Page
	titles   map[string]map[string]*model.Page
	posts    map[string]map[string]*model.Page
	ids      map[string]*model.Page
	rep      Report
}

// Import writes backup b into the XWiki behind c. Content is stored in the
// xhtml/1.0 syntax; links and images point at the imported pages and files.
// Re-running an import overwrites the same pages (idempotent).
func Import(ctx context.Context, c *Client, b *model.Backup, o Options) (Report, error) {
	if o.Wiki == "" {
		o.Wiki = "xwiki"
	}
	if o.Progress == nil {
		o.Progress = func(string, ...any) {}
	}
	im := &importer{c: c, o: o, b: b,
		loc: map[*model.Page][]string{}, spaceLoc: map[*model.Space][]string{}, home: map[*model.Space]*model.Page{},
		titles: map[string]map[string]*model.Page{}, posts: map[string]map[string]*model.Page{}, ids: map[string]*model.Page{},
		rep: Report{Placeholders: map[string]int{}, Unresolved: map[string][]string{}}}
	im.plan()

	if o.DryRun {
		for _, s := range b.Spaces {
			im.rep.Plan = append(im.rep.Plan, strings.Join(im.spaceLoc[s], "/")+"  ← Bereich "+s.Key)
			for _, p := range append(append([]*model.Page{}, s.Pages...), s.Posts...) {
				im.rep.Plan = append(im.rep.Plan, strings.Join(im.loc[p], "/")+"  ← "+p.Title)
			}
		}
		sort.Strings(im.rep.Plan)
		return im.rep, nil
	}

	if err := im.createParents(ctx); err != nil {
		return im.rep, err
	}
	for _, s := range b.Spaces {
		o.Progress("importing space", "key", s.Key, "pages", len(s.Pages), "posts", len(s.Posts))
		if err := im.importSpace(ctx, s); err != nil {
			return im.rep, err
		}
		im.rep.Spaces++
	}
	if o.Verify {
		im.verify(ctx)
	}
	return im.rep, nil
}

// --- naming and locations ---

var unsafeName = regexp.MustCompile(`[./\\:\x00-\x1f\x7f]+`)

// pageName turns a title into an XWiki page name: no separators that XWiki
// references treat specially, at most 100 characters.
func pageName(title, id string) string {
	n := strings.TrimSpace(unsafeName.ReplaceAllString(title, "-"))
	for utf8.RuneCountInString(n) > 100 {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	n = strings.TrimSpace(n)
	if strings.Trim(n, "- ") == "" || strings.EqualFold(n, "WebHome") || strings.EqualFold(n, "WebPreferences") {
		n = "Seite-" + id
	}
	return n
}

func (im *importer) plan() {
	for _, s := range im.b.Spaces {
		im.titles[s.Key] = map[string]*model.Page{}
		im.posts[s.Key] = map[string]*model.Page{}
		for _, p := range s.Pages {
			im.titles[s.Key][p.Title] = p
			im.ids[p.ID] = p
		}
		for _, p := range s.Posts {
			im.posts[s.Key][p.Title] = p
			im.ids[p.ID] = p
		}

		sp := append(append([]string{}, im.o.Parent...), pageName(s.Key, s.ID))
		im.spaceLoc[s] = sp

		// The space homepage becomes the space's own WebHome; its children and
		// all other top-level pages sit directly below the space.
		var top []*model.Page
		if h := s.ByID(s.HomepageID); h != nil && h.Kind == model.KindPage && h.Parent == nil {
			im.home[s] = h
			im.loc[h] = sp
			top = append(top, h.Children...)
		}
		for _, r := range s.Roots {
			if r != im.home[s] {
				top = append(top, r)
			}
		}
		used := map[string]bool{"Blog": len(s.Posts) > 0}
		im.assign(top, sp, used)

		blog := append(append([]string{}, sp...), "Blog")
		usedPosts := map[string]bool{}
		for _, p := range s.Posts {
			name := pageName(dayOf(p.CreatedAt)+" "+p.Title, p.ID)
			if usedPosts[name] {
				name += "-" + p.ID
			}
			usedPosts[name] = true
			im.loc[p] = append(append([]string{}, blog...), name)
		}
	}
}

func (im *importer) assign(ps []*model.Page, parent []string, used map[string]bool) {
	for _, p := range ps {
		name := pageName(p.Title, p.ID)
		if used[name] {
			name += "-" + p.ID
		}
		used[name] = true
		im.loc[p] = append(append([]string{}, parent...), name)
		im.assign(p.Children, im.loc[p], map[string]bool{})
	}
}

func dayOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

func (im *importer) restPage(segs []string) string {
	var b strings.Builder
	b.WriteString("/wikis/" + url.PathEscape(im.o.Wiki))
	for _, s := range segs {
		b.WriteString("/spaces/" + url.PathEscape(s))
	}
	b.WriteString("/pages/WebHome")
	return b.String()
}

func escSegs(segs []string) string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = url.PathEscape(s)
	}
	return strings.Join(out, "/")
}

func (im *importer) viewURL(segs []string) string {
	return im.c.ContextPath() + "/bin/view/" + escSegs(segs) + "/"
}

func (im *importer) downloadURL(segs []string, file string) string {
	return im.c.ContextPath() + "/bin/download/" + escSegs(segs) + "/WebHome/" + url.PathEscape(file)
}

// --- resolver: Confluence references → XWiki URLs ---

type resolver struct {
	im   *importer
	page *model.Page
}

func (r resolver) spaceKey(k string) string {
	if k == "" {
		return r.page.Space.Key
	}
	return k
}

func (r resolver) Page(spaceKey, title string) (string, bool) {
	k := r.spaceKey(spaceKey)
	if title == "" {
		for _, s := range r.im.b.Spaces {
			if s.Key == k {
				return r.im.viewURL(r.im.spaceLoc[s]), true
			}
		}
		return "", false
	}
	if p := r.im.titles[k][title]; p != nil {
		return r.im.viewURL(r.im.loc[p]), true
	}
	return "", false
}

func (r resolver) BlogPost(spaceKey, title, _ string) (string, bool) {
	if p := r.im.posts[r.spaceKey(spaceKey)][title]; p != nil {
		return r.im.viewURL(r.im.loc[p]), true
	}
	return "", false
}

func (r resolver) Content(id string) (string, bool) {
	if p := r.im.ids[id]; p != nil {
		return r.im.viewURL(r.im.loc[p]), true
	}
	return "", false
}

func (r resolver) Attachment(spaceKey, pageTitle, filename string) (string, bool) {
	p := r.page
	if pageTitle != "" {
		k := r.spaceKey(spaceKey)
		if p = r.im.titles[k][pageTitle]; p == nil {
			if p = r.im.posts[k][pageTitle]; p == nil {
				return "", false
			}
		}
	}
	if a, ok := p.AttachmentByName(filename); !ok || a.Path == "" {
		return "", false
	}
	return r.im.downloadURL(r.im.loc[p], filename), true
}

func (r resolver) User(id string) string { return r.im.b.Users[id] }

// --- writing ---

type pageXML struct {
	XMLName xml.Name `xml:"http://www.xwiki.org page"`
	Title   string   `xml:"title"`
	Syntax  string   `xml:"syntax"`
	Content string   `xml:"content"`
}

type tagsXML struct {
	XMLName xml.Name `xml:"http://www.xwiki.org tags"`
	Tags    []tagXML `xml:"tag"`
}

type tagXML struct {
	Name string `xml:"name,attr"`
}

func (im *importer) putPage(ctx context.Context, segs []string, title, content string) error {
	body, err := xml.Marshal(pageXML{Title: title, Syntax: "xhtml/1.0", Content: content})
	if err != nil {
		return err
	}
	return im.c.put(ctx, im.restPage(segs), "application/xml", append([]byte(xml.Header), body...))
}

// createParents creates the optional parent pages (Options.Parent).
func (im *importer) createParents(ctx context.Context) error {
	for i := range im.o.Parent {
		segs := im.o.Parent[:i+1]
		if err := im.putPage(ctx, segs, segs[i], "<p>Aus Confluence übernommene Inhalte.</p>"); err != nil {
			return fmt.Errorf("create parent page %s: %w", strings.Join(segs, "/"), err)
		}
	}
	return nil
}

// fatal reports errors that would make every following request fail too.
func fatal(err error) bool {
	return errors.Is(err, ErrUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (im *importer) importSpace(ctx context.Context, s *model.Space) error {
	sp := im.spaceLoc[s]
	// Space home: the Confluence homepage, or a page with the description.
	if h := im.home[s]; h != nil {
		if err := im.importPage(ctx, h, s.Name); err != nil {
			return err
		}
	} else {
		content := "<p>" + html.EscapeString(s.Description) + "</p>"
		if err := im.putPage(ctx, sp, s.Name, content); err != nil {
			im.fail(s.Key, err)
			if fatal(err) {
				return err
			}
		}
	}
	var walk func([]*model.Page) error
	walk = func(ps []*model.Page) error {
		for _, p := range ps {
			if p != im.home[s] {
				if err := im.importPage(ctx, p, p.Title); err != nil {
					return err
				}
			}
			if err := walk(p.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(s.Roots); err != nil {
		return err
	}

	if len(s.Posts) > 0 {
		blog := append(append([]string{}, sp...), "Blog")
		var list strings.Builder
		list.WriteString("<ul>")
		for _, p := range s.Posts {
			list.WriteString(`<li>` + html.EscapeString(dayOf(p.CreatedAt)) + ` – <a href="` +
				html.EscapeString(im.viewURL(im.loc[p])) + `">` + html.EscapeString(p.Title) + "</a></li>")
		}
		list.WriteString("</ul>")
		if err := im.putPage(ctx, blog, "Blog", list.String()); err != nil {
			im.fail(s.Key+": Blog", err)
			if fatal(err) {
				return err
			}
		}
		for _, p := range s.Posts {
			if err := im.importPage(ctx, p, p.Title); err != nil {
				return err
			}
		}
	}
	return nil
}

func (im *importer) fail(what string, err error) {
	im.rep.Failed = append(im.rep.Failed, what+": "+err.Error())
}

// importPage writes one page/folder/post with its attachments and tags. Only
// errors that make further requests pointless are returned; others are
// recorded and the import continues.
func (im *importer) importPage(ctx context.Context, p *model.Page, title string) error {
	segs := im.loc[p]
	label := p.Space.Key + ": " + p.Title
	content, err := im.content(p)
	if err != nil {
		im.fail(label, err)
		return nil
	}
	if err := im.putPage(ctx, segs, title, content); err != nil {
		im.fail(label, err)
		if fatal(err) {
			return err
		}
		return nil
	}
	switch p.Kind {
	case model.KindFolder:
		im.rep.Folders++
	case model.KindBlogPost:
		im.rep.Posts++
	default:
		im.rep.Pages++
	}

	for _, a := range p.Attachments {
		if a.Path == "" {
			im.rep.MissingFiles++
			continue
		}
		ctype := a.MediaType
		if ctype == "" {
			ctype = mime.TypeByExtension(filepath.Ext(a.Title))
		}
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		rest := im.restPage(segs) + "/attachments/" + url.PathEscape(a.Title)
		if err := im.c.putFile(ctx, rest, ctype, a.Path); err != nil {
			im.fail(label+" / "+a.Title, err)
			if fatal(err) {
				return err
			}
			continue
		}
		im.rep.Attachments++
	}

	tags := tagsXML{Tags: []tagXML{{Name: "confluence"}}}
	for _, l := range p.Labels {
		tags.Tags = append(tags.Tags, tagXML{Name: l})
	}
	if p.Status == "archived" {
		tags.Tags = append(tags.Tags, tagXML{Name: "archiviert"})
	}
	body, _ := xml.Marshal(tags)
	if err := im.c.put(ctx, im.restPage(segs)+"/tags", "application/xml", append([]byte(xml.Header), body...)); err != nil {
		im.fail(label+" (Tags)", err)
		if fatal(err) {
			return err
		}
	}
	if n := im.rep.Pages + im.rep.Posts + im.rep.Folders; n%25 == 0 {
		im.o.Progress("progress", "imported", n)
	}
	return nil
}

// panelClasses maps the converter's panel classes onto XWiki's message boxes.
var panelClasses = strings.NewReplacer(
	`class="panel panel-info"`, `class="box infomessage"`,
	`class="panel panel-note"`, `class="box warningmessage"`,
	`class="panel panel-warning"`, `class="box warningmessage"`,
	`class="panel panel-error"`, `class="box errormessage"`,
	`class="panel panel-tip"`, `class="box successmessage"`,
	`class="panel panel-success"`, `class="box successmessage"`,
	`class="panel panel-panel"`, `class="box"`,
)

func (im *importer) convert(p *model.Page, storage, what string) string {
	res, err := convert.Convert(storage, resolver{im: im, page: p})
	label := p.Space.Key + ": " + p.Title
	if err != nil {
		im.rep.Unresolved[label] = append(im.rep.Unresolved[label], "nur teilweise lesbar"+what+": "+err.Error())
	}
	for m, n := range res.Placeholders {
		im.rep.Placeholders[m] += n
	}
	if len(res.Unresolved) > 0 {
		im.rep.Unresolved[label] = append(im.rep.Unresolved[label], res.Unresolved...)
	}
	return panelClasses.Replace(res.HTML)
}

func (im *importer) user(id string) string {
	if n := im.b.Users[id]; n != "" {
		return n
	}
	if id == "" {
		return "unbekannt"
	}
	return "Konto " + id
}

// content builds the page body: origin note, content, comments.
func (im *importer) content(p *model.Page) (string, error) {
	esc := html.EscapeString
	var b strings.Builder
	origin := fmt.Sprintf("Aus Confluence übernommen (Bereich %s). Ursprünglich erstellt von %s am %s",
		p.Space.Key, im.user(p.AuthorID), dayOf(p.CreatedAt))
	if p.VersionNumber > 0 {
		origin += fmt.Sprintf("; Version %d, zuletzt geändert von %s am %s",
			p.VersionNumber, im.user(p.VersionAuthorID), dayOf(p.VersionCreatedAt))
	}
	b.WriteString(`<p class="confluence-origin"><em>` + esc(origin+".") + "</em></p>")
	if p.Status == "archived" {
		b.WriteString(`<div class="box warningmessage"><p>Diese Seite war in Confluence archiviert.</p></div>`)
	}
	if p.Kind == model.KindFolder {
		b.WriteString("<p>Ordner aus Confluence.</p>")
		return b.String(), nil
	}
	body, err := p.Body()
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	b.WriteString(im.convert(p, body, ""))

	comments, err := p.Comments()
	if err != nil {
		return "", err
	}
	if len(comments) > 0 {
		b.WriteString("<h2>Kommentare aus Confluence</h2>")
		for _, c := range comments {
			b.WriteString(im.comment(p, c))
		}
	}
	return b.String(), nil
}

func (im *importer) comment(p *model.Page, c model.Comment) string {
	esc := html.EscapeString
	var b strings.Builder
	meta := im.user(c.AuthorID) + " am " + dayOf(c.CreatedAt)
	if c.Inline {
		meta += " (Inline-Kommentar"
		if c.Resolution != "" {
			meta += ", " + c.Resolution
		}
		meta += ")"
	}
	b.WriteString(`<div class="confluence-comment"><p><strong>` + esc(meta) + "</strong></p>")
	if c.Selection != "" {
		b.WriteString("<blockquote><p>" + esc(c.Selection) + "</p></blockquote>")
	}
	b.WriteString(im.convert(p, c.Body, " (Kommentar "+c.ID+")"))
	if len(c.Replies) > 0 {
		b.WriteString(`<div class="confluence-replies" style="margin-left: 2em">`)
		for _, r := range c.Replies {
			b.WriteString(im.comment(p, r))
		}
		b.WriteString("</div>")
	}
	b.WriteString("</div>")
	return b.String()
}

// verify checks that every imported page now exists in XWiki.
func (im *importer) verify(ctx context.Context) {
	check := func(segs []string, label string) {
		code, err := im.c.get(ctx, im.restPage(segs))
		if err != nil || code != 200 {
			msg := label
			if err != nil {
				msg += ": " + err.Error()
			} else {
				msg += fmt.Sprintf(": HTTP %d", code)
			}
			im.rep.NotFoundAfterImport = append(im.rep.NotFoundAfterImport, msg)
		}
	}
	for _, s := range im.b.Spaces {
		check(im.spaceLoc[s], s.Key)
		for _, p := range append(append([]*model.Page{}, s.Pages...), s.Posts...) {
			check(im.loc[p], s.Key+": "+p.Title)
		}
	}
}
