package convert

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// fakeResolver resolves pages and attachments it knows about.
type fakeResolver struct{}

func (fakeResolver) Page(space, title string) (string, bool) {
	switch {
	case title == "Ziel" && (space == "" || space == "KB"):
		return "p-ziel.html", true
	case title == "" && space == "OPS":
		return "../OPS/index.html", true
	}
	return "", false
}
func (fakeResolver) BlogPost(space, title, day string) (string, bool) {
	return "blog-" + title + ".html", title == "News"
}
func (fakeResolver) Content(id string) (string, bool) { return "c-" + id + ".html", id == "42" }
func (fakeResolver) Attachment(space, page, file string) (string, bool) {
	if page == "" && (file == "bild.png" || file == "flow.png" || file == "doc.pdf") {
		return "att/" + file, true
	}
	if page == "Andere" && file == "x.png" {
		return "../other/att/x.png", true
	}
	return "", false
}
func (r fakeResolver) Image(space, page, file string) (string, bool) {
	return r.Attachment(space, page, file)
}
func (fakeResolver) User(id string) string {
	if id == "u1" {
		return "Anna Muster"
	}
	return ""
}

func conv(t *testing.T, in string) Result {
	t.Helper()
	res, err := Convert(in, fakeResolver{})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	assertWellFormed(t, res.HTML)
	return res
}

// XWiki imports the result as xhtml/1.0, so it must always be well-formed XML.
func assertWellFormed(t *testing.T, s string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + s + "</root>"))
	d.Strict = true
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("output is not well-formed XML (%v):\n%s", err, s)
		}
	}
}

func contains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func TestConvert_PlainHTMLAndEntities(t *testing.T) {
	res := conv(t, `<h2>Titel</h2><p>A&nbsp;B &amp; C<br>D</p><table><tbody><tr><td colspan="2">x</td></tr></tbody></table>`)
	contains(t, res.HTML, `<h2>Titel</h2>`, "A\u00a0B &amp; C", `<br/>`, `<td colspan="2">x</td>`)
}

func TestConvert_Links(t *testing.T) {
	res := conv(t, `<p>`+
		`<ac:link><ri:page ri:content-title="Ziel"/><ac:plain-text-link-body><![CDATA[zum Ziel]]></ac:plain-text-link-body></ac:link> `+
		`<ac:link><ri:page ri:space-key="KB" ri:content-title="Ziel"/><ac:link-body><strong>fett</strong></ac:link-body></ac:link> `+
		`<ac:link ac:anchor="Abschnitt 2"><ri:page ri:content-title="Ziel"/></ac:link> `+
		`<ac:link ac:anchor="oben"/> `+
		`<ac:link><ri:attachment ri:filename="doc.pdf"/></ac:link> `+
		`<ac:link><ri:user ri:account-id="u1"/></ac:link> `+
		`<ac:link><ri:blog-post ri:content-title="News" ri:posting-day="2026/01/02"/></ac:link> `+
		`<ac:link><ri:content-entity ri:content-id="42"/><ac:plain-text-link-body><![CDATA[Ordner]]></ac:plain-text-link-body></ac:link> `+
		`<ac:link><ri:page ri:content-title="Gibt es nicht"/></ac:link>`+
		`</p>`)
	contains(t, res.HTML,
		`<a href="p-ziel.html">zum Ziel</a>`,
		`<a href="p-ziel.html"><strong>fett</strong></a>`,
		`<a href="p-ziel.html#Abschnitt-2">Ziel</a>`,
		`<a href="#oben">oben</a>`,
		`<a href="att/doc.pdf">doc.pdf</a>`,
		`<span class="user-mention">@Anna Muster</span>`,
		`<a href="blog-News.html">News</a>`,
		`<a href="c-42.html">Ordner</a>`,
		`<span class="unresolved-link">Gibt es nicht</span>`)
	if len(res.Unresolved) != 1 || !strings.Contains(res.Unresolved[0], "Gibt es nicht") {
		t.Errorf("unresolved = %v", res.Unresolved)
	}
}

func TestConvert_Images(t *testing.T) {
	res := conv(t,
		`<ac:image ac:width="300" ac:alt="Bild"><ri:attachment ri:filename="bild.png"/></ac:image>`+
			`<ac:image><ri:attachment ri:filename="x.png"><ri:page ri:content-title="Andere"/></ri:attachment></ac:image>`+
			`<ac:image><ri:url ri:value="https://example.com/a.png"/></ac:image>`+
			`<ac:image><ri:url ri:value="javascript:alert(1)"/></ac:image>`+
			`<ac:image><ri:attachment ri:filename="fehlt.png"/><ac:caption><p>Unterschrift</p></ac:caption></ac:image>`+
			`<ac:image><ri:attachment ri:filename="bild.png"/><ac:caption><p>Mit Text</p></ac:caption></ac:image>`)
	contains(t, res.HTML,
		`<img src="att/bild.png" alt="Bild" width="300"/>`,
		`<img src="../other/att/x.png" alt="x.png"/>`,
		`<img src="https://example.com/a.png" alt="https://example.com/a.png"/>`,
		`[Bild: fehlt.png]`,
		`<figure><img src="att/bild.png" alt="bild.png"/><figcaption><p>Mit Text</p></figcaption></figure>`)
	if strings.Contains(res.HTML, "javascript:") {
		t.Errorf("javascript: URL survived:\n%s", res.HTML)
	}
}

// Code bodies are CDATA; special characters must come out escaped, intact.
func TestConvert_CodeMacro(t *testing.T) {
	res := conv(t, `<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter>`+
		`<ac:parameter ac:name="title">main.go</ac:parameter>`+
		`<ac:plain-text-body><![CDATA[if a < b && c > d {
	fmt.Println("<p>")
}]]></ac:plain-text-body></ac:structured-macro>`)
	contains(t, res.HTML,
		`<p class="code-title">main.go</p>`,
		`<pre><code class="language-go">if a &lt; b &amp;&amp; c &gt; d {`,
		`fmt.Println(&#34;&lt;p&gt;&#34;)`)
}

func TestConvert_PanelsExpandStatusAnchor(t *testing.T) {
	res := conv(t,
		`<ac:structured-macro ac:name="warning"><ac:parameter ac:name="title">Achtung</ac:parameter><ac:rich-text-body><p>Heiss</p></ac:rich-text-body></ac:structured-macro>`+
			`<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">Details</ac:parameter><ac:rich-text-body><p>Versteckt</p></ac:rich-text-body></ac:structured-macro>`+
			`<ac:structured-macro ac:name="status"><ac:parameter ac:name="colour">Green</ac:parameter><ac:parameter ac:name="title">FERTIG</ac:parameter></ac:structured-macro>`+
			`<ac:structured-macro ac:name="anchor"><ac:parameter ac:name="">Teil 2</ac:parameter></ac:structured-macro>`)
	contains(t, res.HTML,
		`<div class="panel panel-warning"><p class="panel-title">Achtung</p><p>Heiss</p></div>`,
		`<details><summary>Details</summary><p>Versteckt</p></details>`,
		`<span class="status status-green">FERTIG</span>`,
		`<a id="Teil-2"></a>`)
}

// Macros without an equivalent keep their content and are reported.
func TestConvert_UnknownMacroKeepsBodyAndIsReported(t *testing.T) {
	res := conv(t, `<ac:structured-macro ac:name="children"/>`+
		`<ac:structured-macro ac:name="some-app-macro"><ac:rich-text-body><p>Wichtiger Inhalt</p></ac:rich-text-body></ac:structured-macro>`+
		`<ac:structured-macro ac:name="toc"/>`)
	contains(t, res.HTML, `[Makro: children]`, `[Makro: some-app-macro]`, `<p>Wichtiger Inhalt</p>`, `[Inhaltsverzeichnis]`)
	if res.Placeholders["children"] != 1 || res.Placeholders["some-app-macro"] != 1 || res.Placeholders["toc"] != 1 {
		t.Errorf("placeholders = %v", res.Placeholders)
	}
}

func TestConvert_TasksEmoticonsLayoutsMarkers(t *testing.T) {
	res := conv(t,
		`<ac:task-list><ac:task><ac:task-id>1</ac:task-id><ac:task-status>complete</ac:task-status><ac:task-body>Erledigt</ac:task-body></ac:task>`+
			`<ac:task><ac:task-id>2</ac:task-id><ac:task-status>incomplete</ac:task-status><ac:task-body>Offen</ac:task-body></ac:task></ac:task-list>`+
			`<p><ac:emoticon ac:name="tick"/> <ac:emoticon ac:name="x" ac:emoji-fallback="🚀"/></p>`+
			`<ac:layout><ac:layout-section ac:type="two_equal"><ac:layout-cell><p>L</p></ac:layout-cell><ac:layout-cell><p>R</p></ac:layout-cell></ac:layout-section></ac:layout>`+
			`<p><ac:inline-comment-marker ac:ref="abc">markiert</ac:inline-comment-marker></p>`+
			`<p><ac:placeholder>Hier tippen</ac:placeholder></p>`)
	contains(t, res.HTML,
		`<li class="task task-complete">☑ Erledigt</li>`, `<li class="task">☐ Offen</li>`,
		`✅`, `🚀`,
		`<div class="layout-section layout-two_equal"><div class="layout-cell"><p>L</p></div>`,
		`<span class="inline-comment-marker" data-ref="abc">markiert</span>`)
	if strings.Contains(res.HTML, "Hier tippen") {
		t.Error("editor placeholder text must not be exported")
	}
}

// Newer-editor content stored as ADF nodes without fallback (seen on a live
// site: decision lists from the meeting-notes template).
func TestConvert_ADFDecisionListAndPanel(t *testing.T) {
	res := conv(t, `<ac:adf-extension><ac:adf-node type="decision-list">`+
		`<ac:adf-attribute key="local-id">x</ac:adf-attribute>`+
		`<ac:adf-node type="decision-item"><ac:adf-attribute key="state">DECIDED</ac:adf-attribute>`+
		`<ac:adf-content>Wir nehmen XWiki</ac:adf-content></ac:adf-node>`+
		`</ac:adf-node></ac:adf-extension>`+
		`<ac:adf-extension><ac:adf-node type="panel"><ac:adf-attribute key="panel-type">note</ac:adf-attribute>`+
		`<ac:adf-content><p>Panel-Text</p></ac:adf-content></ac:adf-node></ac:adf-extension>`+
		`<ac:adf-extension><ac:adf-node type="something-new"><ac:adf-content><p>Neu</p></ac:adf-content></ac:adf-node></ac:adf-extension>`)
	contains(t, res.HTML,
		`<ul class="decision-list"><li class="decision">✔ Entscheidung: Wir nehmen XWiki</li></ul>`,
		`<div class="panel panel-note"><p>Panel-Text</p></div>`,
		`<p>Neu</p>`)
	if strings.Contains(res.HTML, "local-id") || strings.Contains(res.HTML, ">x<") {
		t.Errorf("ADF attributes leaked into the output:\n%s", res.HTML)
	}
	if res.Placeholders["adf:something-new"] != 1 || res.Placeholders["ac:adf-node"] != 0 {
		t.Errorf("placeholders = %v", res.Placeholders)
	}
}

func TestConvert_DrawioUsesPreviewImage(t *testing.T) {
	res := conv(t, `<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">flow</ac:parameter></ac:structured-macro>`)
	contains(t, res.HTML, `<img src="att/flow.png" alt="flow" title="Diagramm: flow"/>`)
}

func TestConvert_Sanitizes(t *testing.T) {
	res := conv(t, `<p onclick="x()" style="color: red; position: fixed; background-image: url(x)">a</p>`+
		`<script>alert(1)</script><a href="javascript:alert(1)">b</a><iframe src="https://x"></iframe>`+
		`<a href="https://ok.example/">c</a>`)
	contains(t, res.HTML, `<p style="color: red">a</p>`, `<a>b</a>`, `<a href="https://ok.example/">c</a>`)
	for _, bad := range []string{"onclick", "alert", "iframe", "position", "url("} {
		if strings.Contains(res.HTML, bad) {
			t.Errorf("%q survived sanitizing:\n%s", bad, res.HTML)
		}
	}
}

func TestConvert_MalformedInputStillProducesWellFormedOutput(t *testing.T) {
	res, _ := Convert(`<p>offen <strong>fett</p><p>danach`, fakeResolver{})
	assertWellFormed(t, res.HTML)
	if !strings.Contains(res.HTML, "fett") || !strings.Contains(res.HTML, "danach") {
		t.Errorf("content lost: %s", res.HTML)
	}
}
