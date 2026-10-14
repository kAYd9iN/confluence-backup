// Package model loads a confluence-backup directory into a structure that
// export and import tools can walk: spaces, their page trees (pages, folders),
// blog posts, attachments, labels, comments and users. It only reads files.
package model

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// Backup is a loaded backup directory.
type Backup struct {
	Dir    string
	Spaces []*Space
	Users  map[string]string // accountId → display name
}

// Space is one Confluence space.
type Space struct {
	ID, Key, Name, Description, HomepageID string
	Dir                                    string
	Roots                                  []*Page // top of the page tree (pages and folders), ordered
	Pages                                  []*Page // every page and folder, flat
	Posts                                  []*Page // blog posts, newest first
	byID                                   map[string]*Page
}

// Kind distinguishes the content types in a space.
type Kind int

const (
	KindPage Kind = iota
	KindFolder
	KindBlogPost
)

// Page is a page, folder or blog post.
type Page struct {
	Kind                                    Kind
	ID, Title, ParentID, ParentType, Status string
	AuthorID, CreatedAt                     string
	VersionNumber                           int
	VersionAuthorID, VersionCreatedAt       string
	Position                                *int
	Space                                   *Space
	Parent                                  *Page
	Children                                []*Page
	Labels                                  []string
	Attachments                             []Attachment
	Dir                                     string // backup directory of this page ("" for folders)
}

// Attachment is an attachment record plus the stored file, if downloaded.
type Attachment struct {
	ID, Title, MediaType, Comment string
	FileSize                      int64
	Path                          string // file in the backup; "" when not downloaded
}

// Comment is a footer or inline comment with its reply thread.
type Comment struct {
	ID, AuthorID, CreatedAt, Body string
	Inline                        bool
	Selection, Resolution         string // inline comments: highlighted text, resolution status
	Replies                       []Comment
}

// Body returns the page's storage-format body (empty for folders).
func (p *Page) Body() (string, error) {
	if p.Dir == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(p.Dir, "index.html")) // #nosec G304 -- inside the backup being loaded
	return string(b), err
}

// Comments returns the page's footer and inline comments.
func (p *Page) Comments() ([]Comment, error) {
	if p.Dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, "comments.json")) // #nosec G304
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Footer []json.RawMessage `json:"footer"`
		Inline []json.RawMessage `json:"inline"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(p.Dir, "comments.json"), err)
	}
	out := parseComments(raw.Footer, false)
	return append(out, parseComments(raw.Inline, true)...), nil
}

func parseComments(items []json.RawMessage, inline bool) []Comment {
	var out []Comment
	for _, it := range items {
		var c struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Version struct {
				AuthorID  string `json:"authorId"`
				CreatedAt string `json:"createdAt"`
			} `json:"version"`
			Body struct {
				Storage struct {
					Value string `json:"value"`
				} `json:"storage"`
			} `json:"body"`
			ResolutionStatus string `json:"resolutionStatus"`
			Properties       struct {
				Selection  string `json:"inlineOriginalSelection"`
				Selection2 string `json:"inline-original-selection"`
			} `json:"properties"`
			Replies []json.RawMessage `json:"replies"`
		}
		if json.Unmarshal(it, &c) != nil {
			continue
		}
		sel := c.Properties.Selection
		if sel == "" {
			sel = c.Properties.Selection2
		}
		out = append(out, Comment{
			ID: c.ID, AuthorID: c.Version.AuthorID, CreatedAt: c.Version.CreatedAt,
			Body: c.Body.Storage.Value, Inline: inline, Selection: sel, Resolution: c.ResolutionStatus,
			Replies: parseComments(c.Replies, inline),
		})
	}
	return out
}

// ByID returns the page, folder or blog post with the given ID in this space.
func (s *Space) ByID(id string) *Page { return s.byID[id] }

// Load reads a backup directory (the timestamped directory containing
// backup-manifest.json).
func Load(dir string) (*Backup, error) {
	b := &Backup{Dir: dir, Users: map[string]string{}}
	if err := b.loadUsers(); err != nil {
		return nil, err
	}
	spaceDirs, err := os.ReadDir(filepath.Join(dir, "spaces"))
	if err != nil {
		return nil, fmt.Errorf("read spaces: %w", err)
	}
	for _, e := range spaceDirs {
		if !e.IsDir() {
			continue
		}
		s, err := loadSpace(filepath.Join(dir, "spaces", e.Name()))
		if err != nil {
			return nil, err
		}
		b.Spaces = append(b.Spaces, s)
	}
	sort.Slice(b.Spaces, func(i, j int) bool { return b.Spaces[i].Key < b.Spaces[j].Key })
	return b, nil
}

func (b *Backup) loadUsers() error {
	data, err := os.ReadFile(filepath.Join(b.Dir, "users.json")) // #nosec G304
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var users []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		PublicName  string `json:"publicName"`
	}
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("parse users.json: %w", err)
	}
	for _, u := range users {
		name := u.DisplayName
		if name == "" {
			name = u.PublicName
		}
		b.Users[u.AccountID] = name
	}
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- inside the backup being loaded
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// contentJSON is the subset of page.json / post.json / folders/*.json used here.
type contentJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ParentID   string `json:"parentId"`
	ParentType string `json:"parentType"`
	Status     string `json:"status"`
	AuthorID   string `json:"authorId"`
	CreatedAt  string `json:"createdAt"`
	Position   *int   `json:"position"`
	Version    struct {
		Number    int    `json:"number"`
		AuthorID  string `json:"authorId"`
		CreatedAt string `json:"createdAt"`
	} `json:"version"`
}

func (c contentJSON) page(kind Kind, dir string, s *Space) *Page {
	return &Page{
		Kind: kind, ID: c.ID, Title: c.Title, ParentID: c.ParentID, ParentType: c.ParentType,
		Status: c.Status, AuthorID: c.AuthorID, CreatedAt: c.CreatedAt, Position: c.Position,
		VersionNumber: c.Version.Number, VersionAuthorID: c.Version.AuthorID,
		VersionCreatedAt: c.Version.CreatedAt, Space: s, Dir: dir,
	}
}

func loadSpace(dir string) (*Space, error) {
	var sj struct {
		ID          string `json:"id"`
		Key         string `json:"key"`
		Name        string `json:"name"`
		HomepageID  string `json:"homepageId"`
		Description struct {
			Plain struct {
				Value string `json:"value"`
			} `json:"plain"`
		} `json:"description"`
	}
	if err := readJSON(filepath.Join(dir, "space.json"), &sj); err != nil {
		return nil, err
	}
	s := &Space{ID: sj.ID, Key: sj.Key, Name: sj.Name, HomepageID: sj.HomepageID,
		Description: sj.Description.Plain.Value, Dir: dir, byID: map[string]*Page{}}

	// Pages: every directory below pages/ that holds a page.json.
	pagesDir := filepath.Join(dir, "pages")
	err := filepath.WalkDir(pagesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == pagesDir {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || d.Name() != "page.json" {
			return nil
		}
		var c contentJSON
		if err := readJSON(path, &c); err != nil {
			return err
		}
		p := c.page(KindPage, filepath.Dir(path), s)
		if err := p.loadExtras(); err != nil {
			return err
		}
		s.add(p)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Folders structure the page tree (pages can have parentType "folder").
	folders, _ := filepath.Glob(filepath.Join(dir, "folders", "*.json"))
	for _, f := range folders {
		var c contentJSON
		if err := readJSON(f, &c); err != nil {
			return nil, err
		}
		s.add(c.page(KindFolder, "", s))
	}

	// Blog posts.
	posts, _ := filepath.Glob(filepath.Join(dir, "blog", "*", "post.json"))
	for _, f := range posts {
		var c contentJSON
		if err := readJSON(f, &c); err != nil {
			return nil, err
		}
		p := c.page(KindBlogPost, filepath.Dir(f), s)
		if err := p.loadExtras(); err != nil {
			return nil, err
		}
		s.byID[p.ID] = p
		s.Posts = append(s.Posts, p)
	}
	sort.SliceStable(s.Posts, func(i, j int) bool { return s.Posts[i].CreatedAt > s.Posts[j].CreatedAt })

	s.link()
	return s, nil
}

func (s *Space) add(p *Page) {
	if _, dup := s.byID[p.ID]; dup {
		return
	}
	s.byID[p.ID] = p
	s.Pages = append(s.Pages, p)
}

// link builds the tree from parent IDs and orders siblings by position, then title.
func (s *Space) link() {
	for _, p := range s.Pages {
		parent := s.byID[p.ParentID]
		if parent != nil && parent != p && parent.Kind != KindBlogPost && !isAncestor(p, parent) {
			p.Parent = parent
			parent.Children = append(parent.Children, p)
		} else {
			s.Roots = append(s.Roots, p)
		}
	}
	// A parent cycle leaves pages unreachable from any root; promote them.
	reached := map[*Page]bool{}
	var mark func(*Page)
	mark = func(p *Page) {
		if reached[p] {
			return
		}
		reached[p] = true
		for _, c := range p.Children {
			mark(c)
		}
	}
	for _, r := range s.Roots {
		mark(r)
	}
	for _, p := range s.Pages {
		if !reached[p] {
			if p.Parent != nil {
				kept := p.Parent.Children[:0]
				for _, c := range p.Parent.Children {
					if c != p {
						kept = append(kept, c)
					}
				}
				p.Parent.Children = kept
				p.Parent = nil
			}
			s.Roots = append(s.Roots, p)
			mark(p)
		}
	}

	order := func(ps []*Page) {
		sort.SliceStable(ps, func(i, j int) bool {
			a, b := ps[i], ps[j]
			if a.Position != nil && b.Position != nil && *a.Position != *b.Position {
				return *a.Position < *b.Position
			}
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		})
	}
	order(s.Roots)
	for _, p := range s.Pages {
		order(p.Children)
	}
	// The homepage leads the roots.
	for i, r := range s.Roots {
		if r.ID == s.HomepageID && i > 0 {
			s.Roots = append([]*Page{r}, append(s.Roots[:i:i], s.Roots[i+1:]...)...)
			break
		}
	}
}

// isAncestor reports whether a is an ancestor of p (guards against cycles).
func isAncestor(a, p *Page) bool {
	for x := p.Parent; x != nil; x = x.Parent {
		if x == a {
			return true
		}
	}
	return false
}

// loadExtras reads labels and attachment records of a page or blog post.
func (p *Page) loadExtras() error {
	var labels []struct {
		Name string `json:"name"`
	}
	if err := readJSON(filepath.Join(p.Dir, "labels.json"), &labels); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range labels {
		p.Labels = append(p.Labels, l.Name)
	}

	var atts []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		MediaType string `json:"mediaType"`
		Comment   string `json:"comment"`
		FileSize  int64  `json:"fileSize"`
	}
	if err := readJSON(filepath.Join(p.Dir, "attachments", "metadata.json"), &atts); err != nil && !os.IsNotExist(err) {
		return err
	}
	files, _ := os.ReadDir(filepath.Join(p.Dir, "attachments", "files"))
	for _, a := range atts {
		att := Attachment{ID: a.ID, Title: a.Title, MediaType: a.MediaType, Comment: a.Comment, FileSize: a.FileSize}
		prefix := storage.SanitizeName(a.ID) + "_"
		for _, f := range files {
			if strings.HasPrefix(f.Name(), prefix) {
				att.Path = filepath.Join(p.Dir, "attachments", "files", f.Name())
				break
			}
		}
		p.Attachments = append(p.Attachments, att)
	}
	return nil
}

// AttachmentByName returns the attachment with the given file name.
func (p *Page) AttachmentByName(name string) (Attachment, bool) {
	for _, a := range p.Attachments {
		if a.Title == name {
			return a, true
		}
	}
	return Attachment{}, false
}
