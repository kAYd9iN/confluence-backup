package backup

import (
	"fmt"

	"github.com/kAYd9iN/confluence-backup/internal/api"
	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// PageNode is a page in the tree together with its children.
type PageNode struct {
	Page     api.Page
	Children []*PageNode
	dirName  string // sanitized, collision-free directory name
}

// DirName returns the sanitized directory name for this page.
func (n *PageNode) DirName() string { return n.dirName }

// BuildTree takes a flat list of pages and returns root nodes
// (pages without a valid parent in the list).
// Orphans (parentID set but parent not in list) are promoted to root.
// Sibling directory names are de-duplicated by appending the page ID.
func BuildTree(pages []api.Page) []*PageNode {
	nodes := make(map[string]*PageNode, len(pages))
	for i := range pages {
		nodes[pages[i].ID] = &PageNode{Page: pages[i]}
	}

	var roots []*PageNode
	for _, n := range nodes {
		parentID := n.Page.ParentID
		if parentID == "" {
			roots = append(roots, n)
			continue
		}
		parent, ok := nodes[parentID]
		if !ok {
			// Orphan — promote to root
			roots = append(roots, n)
			continue
		}
		parent.Children = append(parent.Children, n)
	}

	// Assign collision-free directory names within each sibling group.
	assignDirNames(roots)
	return roots
}

// assignDirNames sets DirName for each node, deduplicating within a sibling group.
func assignDirNames(siblings []*PageNode) {
	seen := make(map[string]int)
	for _, n := range siblings {
		base := storage.SanitizeName(n.Page.Title)
		if base == "" {
			base = fmt.Sprintf("page_%s", n.Page.ID)
		}
		if seen[base] == 0 {
			n.dirName = base
		} else {
			// Collision: append ID to make unique
			n.dirName = fmt.Sprintf("%s_%s", base, n.Page.ID)
		}
		seen[base]++
		// Recurse into children
		assignDirNames(n.Children)
	}
}
