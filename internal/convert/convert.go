// Package convert turns Confluence storage format (XHTML with ac:/ri:
// elements and macros) into clean, self-contained, well-formed XHTML that
// other wikis and browsers understand.
//
// Nothing is dropped silently: macros without an equivalent keep their body
// and get a visible placeholder, and every placeholder and every link that
// could not be resolved is reported in the Result.
package convert

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
)

// Resolver maps Confluence references to URLs in the export target.
// An empty spaceKey means the space of the page being converted; an empty
// pageTitle in Attachment means the page being converted.
type Resolver interface {
	Page(spaceKey, title string) (url string, ok bool)
	BlogPost(spaceKey, title, postingDay string) (url string, ok bool)
	Content(contentID string) (url string, ok bool)
	Attachment(spaceKey, pageTitle, filename string) (url string, ok bool)
	// Image returns the src for an attachment shown as an image. It may differ
	// from Attachment: XWiki, for one, expects an attachment reference there.
	Image(spaceKey, pageTitle, filename string) (src string, ok bool)
	User(accountID string) (displayName string)
}

// Result is the converted page plus everything that needs attention.
type Result struct {
	HTML string
	// Unresolved lists references that could not be turned into a link,
	// e.g. `page "KB:Old title"` or `attachment "diagram.png"`.
	Unresolved []string
	// Placeholders counts macros rendered as a placeholder, by macro name.
	Placeholders map[string]int
}

// Convert converts a storage-format body. It never fails on unknown content;
// an error is returned only when the markup cannot be parsed at all, together
// with the HTML of everything parsed up to that point.
func Convert(storage string, r Resolver) (Result, error) {
	root, perr := parse(storage)
	c := &converter{r: r, res: Result{Placeholders: map[string]int{}}}
	c.children(root)
	c.res.HTML = c.b.String()
	sort.Strings(c.res.Unresolved)
	if perr != nil {
		return c.res, fmt.Errorf("storage format only partially parseable: %w", perr)
	}
	return c.res, nil
}

// --- parsing ---

type node struct {
	name     xml.Name // Space is the prefix ("ac", "ri") or "" for HTML
	attr     []xml.Attr
	children []*node
	text     string // text node when name.Local == ""
}

func (n *node) isText() bool { return n.name.Local == "" }

func (n *node) is(space, local string) bool {
	return n.name.Space == space && strings.EqualFold(n.name.Local, local)
}

func (n *node) get(space, local string) string {
	for _, a := range n.attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (n *node) child(space, local string) *node {
	for _, c := range n.children {
		if c.is(space, local) {
			return c
		}
	}
	return nil
}

// textContent returns all text below n.
func (n *node) textContent() string {
	if n.isText() {
		return n.text
	}
	var b strings.Builder
	for _, c := range n.children {
		b.WriteString(c.textContent())
	}
	return b.String()
}

// parse builds a tree from storage format. encoding/xml in non-strict mode
// keeps undeclared prefixes (ac:, ri:) as Name.Space, understands CDATA
// (code macro bodies) and, with HTMLEntity, &nbsp; and friends.
func parse(storage string) (*node, error) {
	d := xml.NewDecoder(strings.NewReader("<root>" + storage + "</root>"))
	d.Strict = false
	// Not xml.HTMLAutoClose: it lists "link" and "param", and AutoClose
	// matches the local name only — <ac:link> would be treated as void.
	d.AutoClose = []string{"br", "hr", "img", "col", "input", "meta", "area", "base", "wbr", "source", "track"}
	d.Entity = xml.HTMLEntity

	root := &node{name: xml.Name{Local: "root"}}
	stack := []*node{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return root, nil
		}
		if err != nil {
			return root, err
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name, attr: append([]xml.Attr(nil), t.Attr...)}
			top.children = append(top.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			// Pop to the matching element (tolerates mis-nesting).
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].name.Local == t.Name.Local {
					stack = stack[:i]
					break
				}
			}
		case xml.CharData:
			top.children = append(top.children, &node{text: string(t)})
		}
	}
}

// --- rendering ---

type converter struct {
	r   Resolver
	b   strings.Builder
	res Result
}

func (c *converter) text(s string) { c.b.WriteString(html.EscapeString(s)) }

func (c *converter) open(tag string, attrs ...string) {
	c.b.WriteString("<" + tag)
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] == "" {
			continue
		}
		c.b.WriteString(" " + attrs[i] + `="` + html.EscapeString(attrs[i+1]) + `"`)
	}
	c.b.WriteString(">")
}

func (c *converter) close(tag string) { c.b.WriteString("</" + tag + ">") }

func (c *converter) void(tag string, attrs ...string) {
	c.open(tag, attrs...)
	s := c.b.String()
	c.b.Reset()
	c.b.WriteString(s[:len(s)-1] + "/>")
}

func (c *converter) unresolved(what string) {
	c.res.Unresolved = append(c.res.Unresolved, what)
}

func (c *converter) placeholder(macro, label string) {
	c.res.Placeholders[macro]++
	c.open("p", "class", "macro-placeholder")
	c.text("[" + label + "]")
	c.close("p")
}

func (c *converter) children(n *node) {
	for _, ch := range n.children {
		c.node(ch)
	}
}

func (c *converter) node(n *node) {
	switch {
	case n.isText():
		c.text(n.text)
	case n.name.Space == "":
		c.htmlElement(n)
	case n.name.Space == "ac":
		c.acElement(n)
	default:
		// ri:* outside of a link/image, or an unknown prefix: no visible content.
	}
}

// Allowed HTML elements; anything else renders only its children, and the
// dangerous ones render nothing.
var allowed = map[string]bool{
	"p": true, "br": true, "hr": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"strong": true, "b": true, "em": true, "i": true, "u": true, "s": true, "del": true, "strike": true,
	"sub": true, "sup": true, "code": true, "pre": true, "blockquote": true, "span": true, "div": true,
	"a": true, "img": true, "ul": true, "ol": true, "li": true, "table": true, "thead": true, "tbody": true,
	"tfoot": true, "tr": true, "th": true, "td": true, "colgroup": true, "col": true, "caption": true,
	"dl": true, "dt": true, "dd": true, "small": true, "mark": true, "abbr": true, "cite": true, "q": true,
	"time": true, "details": true, "summary": true, "figure": true, "figcaption": true,
}

var dropped = map[string]bool{"script": true, "style": true, "iframe": true, "object": true,
	"embed": true, "form": true, "input": true, "button": true, "textarea": true, "select": true, "link": true, "meta": true}

var voidElements = map[string]bool{"br": true, "hr": true, "img": true, "col": true}

// Attributes kept per element (plus the global ones below).
var allowedAttrs = map[string][]string{
	"a": {"href", "name"}, "img": {"src", "alt", "width", "height"},
	"td": {"colspan", "rowspan"}, "th": {"colspan", "rowspan"}, "col": {"span"},
	"ol": {"start"}, "time": {"datetime"},
}
var globalAttrs = []string{"title", "id", "style"}

func (c *converter) htmlElement(n *node) {
	tag := strings.ToLower(n.name.Local)
	if dropped[tag] {
		return
	}
	if !allowed[tag] {
		c.children(n)
		return
	}
	var attrs []string
	for _, name := range append(append([]string{}, globalAttrs...), allowedAttrs[tag]...) {
		v := n.get("", name)
		switch name {
		case "href", "src":
			v = safeURL(v)
		case "style":
			v = safeStyle(v)
		}
		attrs = append(attrs, name, v)
	}
	if tag == "time" && len(n.children) == 0 {
		c.open("time", attrs...)
		c.text(n.get("", "datetime"))
		c.close("time")
		return
	}
	if voidElements[tag] {
		c.void(tag, attrs...)
		return
	}
	c.open(tag, attrs...)
	c.children(n)
	c.close(tag)
}

// safeURL keeps web, mail and relative links and drops everything else
// (javascript:, data:, …).
func safeURL(u string) string {
	t := strings.TrimSpace(u)
	l := strings.ToLower(t)
	switch {
	case l == "":
		return ""
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"), strings.HasPrefix(l, "mailto:"),
		strings.HasPrefix(l, "tel:"), strings.HasPrefix(l, "#"), strings.HasPrefix(l, "/"),
		strings.HasPrefix(l, "./"), strings.HasPrefix(l, "../"):
		return t
	case !strings.Contains(l, ":"):
		return t // relative path
	}
	return ""
}

// safeStyle keeps only presentational properties that carry meaning
// (alignment, colours, widths) and drops the rest.
func safeStyle(s string) string {
	keep := map[string]bool{"text-align": true, "color": true, "background-color": true, "width": true,
		"text-decoration": true, "margin-left": true, "padding-left": true}
	var out []string
	for _, decl := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(decl, ":")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if ok && keep[k] && !strings.ContainsAny(v, "()\\<>") {
			out = append(out, k+": "+v)
		}
	}
	return strings.Join(out, "; ")
}

func (c *converter) acElement(n *node) {
	switch strings.ToLower(n.name.Local) {
	case "link":
		c.link(n)
	case "image":
		c.image(n)
	case "structured-macro", "macro":
		c.macro(n)
	case "task-list":
		c.taskList(n)
	case "emoticon":
		c.emoticon(n)
	case "layout":
		c.wrap(n, "layout")
	case "layout-section":
		c.wrap(n, "layout-section layout-"+n.get("ac", "type"))
	case "layout-cell":
		c.wrap(n, "layout-cell")
	case "inline-comment-marker":
		c.open("span", "class", "inline-comment-marker", "data-ref", n.get("ac", "ref"))
		c.children(n)
		c.close("span")
	case "placeholder", "parameter", "task-id", "task-uuid", "adf-attribute":
		// Editor instructions and macro internals: no visible content.
	case "adf-extension":
		if fb := n.child("ac", "adf-fallback"); fb != nil {
			c.children(fb)
		} else {
			c.children(n)
		}
	default:
		// Unknown ac: element: keep whatever it contains.
		c.res.Placeholders["ac:"+n.name.Local]++
		c.children(n)
	}
}

func (c *converter) wrap(n *node, class string) {
	c.open("div", "class", class)
	c.children(n)
	c.close("div")
}

// target resolves the ri: reference inside a link or (image=true) an image.
func (c *converter) target(n *node, image bool) (url, label string, ok bool) {
	if p := n.child("ri", "page"); p != nil {
		space, title := p.get("ri", "space-key"), p.get("ri", "content-title")
		url, ok = c.r.Page(space, title)
		if !ok {
			c.unresolved(fmt.Sprintf("page %q", qualified(space, title)))
		}
		return url, title, ok
	}
	if p := n.child("ri", "blog-post"); p != nil {
		space, title, day := p.get("ri", "space-key"), p.get("ri", "content-title"), p.get("ri", "posting-day")
		url, ok = c.r.BlogPost(space, title, day)
		if !ok {
			c.unresolved(fmt.Sprintf("blog post %q", qualified(space, title)))
		}
		return url, title, ok
	}
	if p := n.child("ri", "content-entity"); p != nil {
		id := p.get("ri", "content-id")
		url, ok = c.r.Content(id)
		if !ok {
			c.unresolved(fmt.Sprintf("content %q", id))
		}
		return url, "", ok
	}
	if a := n.child("ri", "attachment"); a != nil {
		return c.attachment(a, image)
	}
	if u := n.child("ri", "url"); u != nil {
		v := safeURL(u.get("ri", "value"))
		return v, v, v != ""
	}
	if s := n.child("ri", "space"); s != nil {
		key := s.get("ri", "space-key")
		url, ok = c.r.Page(key, "")
		if !ok {
			c.unresolved(fmt.Sprintf("space %q", key))
		}
		return url, key, ok
	}
	return "", "", false
}

// attachment resolves ri:attachment, optionally on another page (nested
// ri:page), as a link target or (image=true) as an image source.
func (c *converter) attachment(a *node, image bool) (url, label string, ok bool) {
	file := a.get("ri", "filename")
	space, page := "", ""
	if p := a.child("ri", "page"); p != nil {
		space, page = p.get("ri", "space-key"), p.get("ri", "content-title")
	} else if p := a.child("ri", "blog-post"); p != nil {
		space, page = p.get("ri", "space-key"), p.get("ri", "content-title")
	}
	if image {
		url, ok = c.r.Image(space, page, file)
	} else {
		url, ok = c.r.Attachment(space, page, file)
	}
	if !ok {
		where := file
		if page != "" {
			where = qualified(space, page) + "/" + file
		}
		c.unresolved(fmt.Sprintf("attachment %q", where))
	}
	return url, file, ok
}

func qualified(space, title string) string {
	if space == "" {
		return title
	}
	return space + ":" + title
}

func (c *converter) link(n *node) {
	if u := n.child("ri", "user"); u != nil {
		name := c.r.User(u.get("ri", "account-id"))
		if name == "" {
			name = "unbekannter Benutzer"
		}
		c.open("span", "class", "user-mention")
		c.text("@" + name)
		c.close("span")
		return
	}
	url, label, ok := c.target(n, false)
	if anchor := n.get("ac", "anchor"); anchor != "" {
		if !ok && url == "" && n.child("ri", "page") == nil && n.child("ri", "attachment") == nil {
			url, ok = "", true // anchor on the same page
		}
		url += "#" + anchorID(anchor)
		if label == "" {
			label = anchor
		}
	}
	if ok {
		c.open("a", "href", url)
	} else {
		c.open("span", "class", "unresolved-link")
	}
	switch body := n.child("ac", "link-body"); {
	case body != nil:
		c.children(body)
	case n.child("ac", "plain-text-link-body") != nil:
		c.text(n.child("ac", "plain-text-link-body").textContent())
	default:
		c.text(label)
	}
	if ok {
		c.close("a")
	} else {
		c.close("span")
	}
}

// anchorID normalizes an anchor name the way it is referenced by links.
func anchorID(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), " ", "-")
}

func (c *converter) image(n *node) {
	url, label, ok := c.target(n, true)
	if !ok {
		c.open("span", "class", "unresolved-image")
		c.text("[Bild: " + label + "]")
		c.close("span")
		return
	}
	alt := n.get("ac", "alt")
	if alt == "" {
		alt = label
	}
	attrs := []string{"src", url, "alt", alt, "title", n.get("ac", "title"),
		"width", n.get("ac", "width"), "height", n.get("ac", "height")}
	caption := n.child("ac", "caption")
	if caption == nil {
		c.void("img", attrs...)
		return
	}
	c.open("figure")
	c.void("img", attrs...)
	c.open("figcaption")
	c.children(caption)
	c.close("figcaption")
	c.close("figure")
}

// params returns a macro's parameters; ac:parameter with an empty name is
// the default parameter (key "").
func params(n *node) map[string]*node {
	m := map[string]*node{}
	for _, ch := range n.children {
		if ch.is("ac", "parameter") {
			m[ch.get("ac", "name")] = ch
		}
	}
	return m
}

func param(p map[string]*node, name string) string {
	if n := p[name]; n != nil {
		return strings.TrimSpace(n.textContent())
	}
	return ""
}

var panelMacros = map[string]bool{"info": true, "tip": true, "note": true, "warning": true,
	"panel": true, "success": true, "error": true}

var bodyMacros = map[string]bool{"excerpt": true, "details": true, "section": true, "column": true,
	"div": true, "center": true, "span": true}

func (c *converter) macro(n *node) {
	name := strings.ToLower(n.get("ac", "name"))
	p := params(n)
	rich := n.child("ac", "rich-text-body")
	plain := n.child("ac", "plain-text-body")

	switch {
	case name == "code" || name == "noformat":
		if t := param(p, "title"); t != "" {
			c.open("p", "class", "code-title")
			c.text(t)
			c.close("p")
		}
		lang := param(p, "language")
		c.open("pre")
		if lang != "" {
			c.open("code", "class", "language-"+lang)
		} else {
			c.open("code")
		}
		if plain != nil {
			c.text(plain.textContent())
		} else if rich != nil {
			c.text(rich.textContent())
		}
		c.close("code")
		c.close("pre")

	case panelMacros[name]:
		c.open("div", "class", "panel panel-"+name)
		if t := param(p, "title"); t != "" {
			c.open("p", "class", "panel-title")
			c.text(t)
			c.close("p")
		}
		if rich != nil {
			c.children(rich)
		}
		c.close("div")

	case name == "expand":
		title := param(p, "title")
		if title == "" {
			title = "Mehr anzeigen"
		}
		c.open("details")
		c.open("summary")
		c.text(title)
		c.close("summary")
		if rich != nil {
			c.children(rich)
		}
		c.close("details")

	case name == "status":
		title := param(p, "title")
		if title == "" {
			title = param(p, "colour")
		}
		c.open("span", "class", "status status-"+strings.ToLower(param(p, "colour")))
		c.text(title)
		c.close("span")

	case name == "anchor":
		c.open("a", "id", anchorID(param(p, "")))
		c.close("a")

	case name == "view-file" || name == "viewpdf" || name == "viewdoc" || name == "viewxls" ||
		name == "viewppt" || name == "multimedia" || name == "view-file-macro":
		if pn := p["name"]; pn != nil {
			if a := pn.child("ri", "attachment"); a != nil {
				url, file, ok := c.attachment(a, false)
				if ok {
					c.open("p", "class", "attachment-link")
					c.open("a", "href", url)
					c.text(file)
					c.close("a")
					c.close("p")
					return
				}
			}
		}
		c.placeholder(name, "Datei-Vorschau")

	case name == "drawio" || name == "inc-drawio" || name == "gliffy":
		diagram := param(p, "diagramName")
		if diagram == "" {
			diagram = param(p, "name")
		}
		if url, ok := c.r.Image("", "", diagram+".png"); ok && diagram != "" {
			c.void("img", "src", url, "alt", diagram, "title", "Diagramm: "+diagram)
			return
		}
		c.placeholder(name, "Diagramm: "+diagram)

	case name == "jira":
		key := param(p, "key")
		if key == "" {
			key = param(p, "jqlQuery")
		}
		c.placeholder(name, "Jira: "+key)

	case name == "toc":
		c.placeholder(name, "Inhaltsverzeichnis")

	case bodyMacros[name]:
		c.open("div", "class", "macro-"+name)
		if rich != nil {
			c.children(rich)
		}
		c.close("div")

	default:
		// No equivalent (children, pagetree, include, chart, …): visible
		// placeholder, and any body content is kept.
		c.placeholder(name, "Makro: "+name)
		if rich != nil {
			c.children(rich)
		} else if plain != nil {
			c.open("pre")
			c.text(plain.textContent())
			c.close("pre")
		}
	}
}

func (c *converter) taskList(n *node) {
	c.open("ul", "class", "task-list")
	for _, t := range n.children {
		if !t.is("ac", "task") {
			continue
		}
		done := false
		if s := t.child("ac", "task-status"); s != nil {
			done = strings.TrimSpace(s.textContent()) == "complete"
		}
		class, box := "task", "☐ "
		if done {
			class, box = "task task-complete", "☑ "
		}
		c.open("li", "class", class)
		c.text(box)
		if body := t.child("ac", "task-body"); body != nil {
			c.children(body)
		}
		c.close("li")
	}
	c.close("ul")
}

var emoticons = map[string]string{
	"smile": "🙂", "sad": "🙁", "cheeky": "😛", "laugh": "😃", "wink": "😉", "thumbs-up": "👍",
	"thumbs-down": "👎", "information": "ℹ️", "tick": "✅", "cross": "❌", "warning": "⚠️",
	"plus": "➕", "minus": "➖", "question": "❓", "light-on": "💡", "light-off": "💡",
	"yellow-star": "⭐", "red-star": "⭐", "green-star": "⭐", "blue-star": "⭐",
	"heart": "❤️", "broken-heart": "💔",
}

func (c *converter) emoticon(n *node) {
	if fb := n.get("ac", "emoji-fallback"); fb != "" {
		c.text(fb)
		return
	}
	if e, ok := emoticons[n.get("ac", "name")]; ok {
		c.text(e)
		return
	}
	if sn := n.get("ac", "emoji-shortname"); sn != "" {
		c.text(sn)
		return
	}
	c.text(":" + n.get("ac", "name") + ":")
}
