// Package skills serves self-documenting help topics embedded in the binary, so an
// operator or agent can discover how the tool works without external docs. Topics are
// markdown files with a small frontmatter block (id/title/description/order), compiled
// in via go:embed — the single source of truth for every documentation surface.
package skills

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed topics/*.md
var topicFS embed.FS

// Topic is one help document.
type Topic struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Order       int    `json:"order"`
	Body        string `json:"body,omitempty"`
}

// All returns every topic (with body), sorted by frontmatter order then id.
func All() []Topic {
	entries, err := topicFS.ReadDir("topics")
	if err != nil {
		return nil
	}
	var out []Topic
	for _, e := range entries {
		b, err := topicFS.ReadFile("topics/" + e.Name())
		if err != nil {
			continue
		}
		t := parseTopic(strings.TrimSuffix(e.Name(), ".md"), string(b))
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns the topic by id, or an error naming the available topics.
func Get(id string) (Topic, error) {
	for _, t := range All() {
		if t.ID == id {
			return t, nil
		}
	}
	var ids []string
	for _, t := range All() {
		ids = append(ids, t.ID)
	}
	return Topic{}, fmt.Errorf("unknown topic %q; available: %s", id, strings.Join(ids, ", "))
}

// parseTopic splits an optional "---" frontmatter block (id/title/description/order)
// from the markdown body. A file without frontmatter still works: the filename is the
// id and the body is everything.
func parseTopic(id, raw string) Topic {
	t := Topic{ID: id, Body: raw}
	rest, ok := strings.CutPrefix(raw, "---\n")
	if !ok {
		return t
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return t
	}
	t.Body = strings.TrimPrefix(body, "\n")
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "id":
			t.ID = v
		case "title":
			t.Title = v
		case "description":
			t.Description = v
		case "order":
			if n, err := strconv.Atoi(v); err == nil {
				t.Order = n
			}
		}
	}
	return t
}
