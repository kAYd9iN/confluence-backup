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

	// Walk in API order, not map order, so sibling order — and with it which
	// of two same-titled pages gets the ID suffix — is stable between runs.
	var roots []*PageNode
	placed := make(map[string]bool, len(pages))
	for i := range pages {
		id := pages[i].ID
		if placed[id] {
			continue // the API listed the same page twice
		}
		placed[id] = true
		n := nodes[id]
		parentID := n.Page.ParentID
		if parentID == "" || parentID == id {
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

	// A parent cycle (A under B, B under A) leaves pages unreachable from any
	// root; they would silently not be written. Promote one page per cycle.
	reached := map[*PageNode]bool{}
	var mark func(*PageNode)
	mark = func(n *PageNode) {
		if reached[n] {
			return
		}
		reached[n] = true
		for _, c := range n.Children {
			mark(c)
		}
	}
	for _, r := range roots {
		mark(r)
	}
	for i := range pages {
		if n := nodes[pages[i].ID]; !reached[n] {
			// Detach n from its (cyclic) parent and make it a root.
			if p := nodes[n.Page.ParentID]; p != nil {
				p.Children = removeNode(p.Children, n)
			}
			roots = append(roots, n)
			mark(n)
		}
	}

	// Assign collision-free directory names within each sibling group.
	assignDirNames(roots)
	return roots
}

func removeNode(nodes []*PageNode, n *PageNode) []*PageNode {
	out := nodes[:0]
	for _, x := range nodes {
		if x != n {
			out = append(out, x)
		}
	}
	return out
}

// assignDirNames sets DirName for each node, deduplicating within a sibling group.
func assignDirNames(siblings []*PageNode) {
	seen := make(map[string]int)
	for _, n := range siblings {
		base := safeName(n.Page.Title, 80)
		if base == "" {
			base = fmt.Sprintf("page_%s", storage.SanitizeName(n.Page.ID))
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
