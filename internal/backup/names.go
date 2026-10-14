package backup

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/kAYd9iN/confluence-backup/internal/storage"
)

// safeName turns a title into a file/directory name: unsafe characters are
// replaced, the result is capped at max bytes (long titles in deep page trees
// produce paths that other tools — archivers, Explorer — cannot handle), and
// trailing dots are removed (Windows drops them silently). It returns "" when
// nothing usable is left — including "." and "..", which as directory names
// would write a page into its parent's directory.
func safeName(title string, max int) string {
	s := storage.SanitizeName(title)
	if len(s) > max {
		s = s[:max]
	}
	s = strings.TrimRight(s, ".")
	if strings.Trim(s, "._") == "" {
		return ""
	}
	return s
}

// userSet collects every account ID referenced by backed-up objects, so
// users.json can map authors, owners and commenters to names on restore.
type userSet struct {
	mu  sync.Mutex
	set map[string]struct{}
}

func newUserSet() *userSet { return &userSet{set: map[string]struct{}{}} }

// accountFields are the fields of Confluence objects that hold account IDs.
var accountFields = []string{"authorId", "ownerId", "lastOwnerId", "spaceOwnerId",
	"createdBy", "assignedTo", "completedBy"}

// addFrom records the account IDs of one raw object (including version.authorId).
func (u *userSet) addFrom(raw json.RawMessage) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.addFields(obj)
	var version map[string]json.RawMessage
	if json.Unmarshal(obj["version"], &version) == nil {
		u.addFields(version)
	}
}

// addFromTree records the account IDs of a comment and its whole reply thread.
func (u *userSet) addFromTree(raw json.RawMessage) {
	u.addFrom(raw)
	var obj struct {
		Replies []json.RawMessage `json:"replies"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, r := range obj.Replies {
			u.addFromTree(r)
		}
	}
}

func (u *userSet) addFields(obj map[string]json.RawMessage) {
	for _, f := range accountFields {
		var id string
		if json.Unmarshal(obj[f], &id) == nil && id != "" {
			u.set[id] = struct{}{}
		}
	}
}

// ids returns the collected account IDs in sorted order.
func (u *userSet) ids() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]string, 0, len(u.set))
	for id := range u.set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
