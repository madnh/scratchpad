package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// setup wires a real store behind an in-process MCP server and returns a connected
// client session plus the store (for out-of-band mutations, e.g. waking a waiter).
func setup(t *testing.T) (*mcp.ClientSession, *store.Store) {
	t.Helper()
	ctx := context.Background()

	cfg := config.Config{
		DefaultProject: "default",
		Limits:         config.DefaultLimits,
		Wait:           config.Wait{DefaultS: 1, MaxS: 2}, // keep tests fast
	}
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	st := store.New(dir, projects, cfg.Limits)

	ms := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	New(st, cfg).AddTools(ms)

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := ms.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil).
		Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, st
}

// call invokes a tool and decodes its structured output into v, failing on tool error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, v any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("tool %s returned error: %v", name, res.Content)
	}
	if v != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured output: %v", err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("unmarshal into %T: %v", v, err)
		}
	}
}

// callErr invokes a tool expecting a tool error and returns its text.
func callErr(t *testing.T, cs *mcp.ClientSession, name string, args any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("tool %s: expected an error result", name)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolSurface(t *testing.T) {
	cs, _ := setup(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The surface is fixed and append-only: pad_tasks and pad_rules READ derived state,
	// and a task or a pad's rules are written through pad_post. A pad_task_update or a
	// rules-writing tool appearing here would mean the agent surface had stopped being
	// append-only.
	want := map[string]bool{
		"pad_create": false, "pad_post": false, "pad_get": false, "pad_read": false,
		"pad_wait": false, "pad_tasks": false, "pad_rules": false, "pad_list": false,
		"project_list": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool %q (the surface is exactly the 9 agreed tools)", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestCreatePostGetReadFlow(t *testing.T) {
	cs, _ := setup(t)

	var created createOutput
	call(t, cs, "pad_create", map[string]any{
		"author": "frontend", "title": "How does API X work", "content": "the question",
	}, &created)
	if created.Project != "default" || created.Section != 1 || created.Next != 2 {
		t.Fatalf("bad create output: %+v", created)
	}
	if created.Password != "" {
		t.Fatalf("unprotected create returned a password")
	}
	if created.Turn.LastAuthor != "frontend" {
		t.Fatalf("bad turn: %+v", created.Turn)
	}

	// Turn rule over MCP.
	msg := callErr(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "frontend", "title": "again", "content": "x",
	})
	if !strings.Contains(msg, store.CodeNotYourTurn) {
		t.Fatalf("want not_your_turn, got %q", msg)
	}

	var posted postOutput
	call(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "backend", "title": "Answer", "content": "the answer",
	}, &posted)
	if posted.Section != 2 || posted.Next != 3 {
		t.Fatalf("bad post output: %+v", posted)
	}

	// pad_get: TOC only, no content.
	var got getOutput
	call(t, cs, "pad_get", map[string]any{"ref": created.Ref}, &got)
	if got.SectionCount != 2 || got.LastAuthor != "backend" {
		t.Fatalf("bad get output: %+v", got)
	}
	// The roster: everyone who has posted, in first-appearance order.
	if len(got.Authors) != 2 || got.Authors[0] != "frontend" || got.Authors[1] != "backend" {
		t.Fatalf("bad authors: %v", got.Authors)
	}
	for _, sec := range got.Sections {
		if sec.Content != "" {
			t.Fatalf("pad_get must not return content: %+v", sec)
		}
	}

	// pad_read: since selects newer sections, with content.
	var read readOutput
	call(t, cs, "pad_read", map[string]any{"ref": created.Ref, "since": 1}, &read)
	if len(read.Sections) != 1 || read.Sections[0].N != 2 || !strings.Contains(read.Sections[0].Content, "the answer") {
		t.Fatalf("bad read output: %+v", read.Sections)
	}
	// section selection
	call(t, cs, "pad_read", map[string]any{"ref": created.Ref, "section": 1}, &read)
	if len(read.Sections) != 1 || read.Sections[0].N != 1 {
		t.Fatalf("bad section read: %+v", read.Sections)
	}
	// both → invalid_input
	if msg := callErr(t, cs, "pad_read", map[string]any{"ref": created.Ref, "section": 1, "since": 1}); !strings.Contains(msg, store.CodeInvalidInput) {
		t.Fatalf("want invalid_input, got %q", msg)
	}
}

func TestProtectedPadOverMCP(t *testing.T) {
	cs, _ := setup(t)
	var created createOutput
	call(t, cs, "pad_create", map[string]any{
		"author": "a", "title": "secret", "content": "c", "protect": true,
	}, &created)
	if created.Password == "" {
		t.Fatal("protect:true must return a generated password")
	}
	if msg := callErr(t, cs, "pad_read", map[string]any{"ref": created.Ref}); !strings.Contains(msg, store.CodeUnauthorized) {
		t.Fatalf("want unauthorized, got %q", msg)
	}
	if msg := callErr(t, cs, "pad_read", map[string]any{"ref": created.Ref, "password": "wrong"}); !strings.Contains(msg, store.CodeUnauthorized) {
		t.Fatalf("want unauthorized, got %q", msg)
	}
	var read readOutput
	call(t, cs, "pad_read", map[string]any{"ref": created.Ref, "password": created.Password}, &read)
	if len(read.Sections) != 1 {
		t.Fatalf("bad protected read: %+v", read)
	}
	// Metadata stays visible without the password.
	var list listOutput
	call(t, cs, "pad_list", nil, &list)
	if len(list.Pads) != 1 || !list.Pads[0].Protected {
		t.Fatalf("protected pad missing from list: %+v", list)
	}
}

func TestWaitTimeoutAndChange(t *testing.T) {
	cs, st := setup(t)
	var created createOutput
	call(t, cs, "pad_create", map[string]any{"author": "a", "title": "t", "content": "c"}, &created)

	// Timeout: changed=false, NOT a tool error (cfg caps the wait at 2s).
	start := time.Now()
	var w waitOutput
	call(t, cs, "pad_wait", map[string]any{"ref": created.Ref, "since": 1, "timeout_s": 600}, &w)
	if w.Changed {
		t.Fatalf("nothing was posted; want changed=false: %+v", w)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout_s was not clamped to the 2s cap (took %s)", elapsed)
	}
	if w.SectionCount != 1 || w.LastAuthor != "a" {
		t.Fatalf("timeout must still return compact state: %+v", w)
	}

	// Change: a concurrent post (via the shared storage layer, as a CLI would) wakes it.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = st.Post(store.PostRequest{Ref: created.Ref, Author: "b", Title: "reply", Content: "answer", Password: ""})
	}()
	call(t, cs, "pad_wait", map[string]any{"ref": created.Ref, "since": 1}, &w)
	if !w.Changed || len(w.Sections) != 1 || w.Sections[0].N != 2 {
		t.Fatalf("want the new section: %+v", w)
	}
	if !strings.Contains(w.Sections[0].Content, "answer") {
		t.Fatalf("wait must deliver content: %+v", w.Sections[0])
	}
}

func TestListAndProjects(t *testing.T) {
	cs, _ := setup(t)
	for i := 0; i < 3; i++ {
		project := "p1"
		if i == 2 {
			project = "p2"
		}
		call(t, cs, "pad_create", map[string]any{
			"project": project, "author": "a", "title": fmt.Sprintf("pad %d", i), "content": "c",
		}, nil)
	}
	var list listOutput
	call(t, cs, "pad_list", map[string]any{"project": "p1"}, &list)
	if len(list.Pads) != 2 {
		t.Fatalf("want 2 pads in p1: %+v", list)
	}
	var projects projectListOutput
	call(t, cs, "project_list", nil, &projects)
	if len(projects.Projects) != 2 {
		t.Fatalf("want 2 projects: %+v", projects)
	}
}

// The agent-facing side of rules: read them with pad_rules, quote the digest on the
// first post, and set a pad's own with pad_post(set_rules) — which stays an append, so
// the surface is still append-only.
func TestRulesOverMCP(t *testing.T) {
	cs, st := setup(t)
	if err := st.SetStoreRules("- keep it under 15 lines", false); err != nil {
		t.Fatal(err)
	}

	var rules rulesOutput
	call(t, cs, "pad_rules", map[string]any{"project": "default"}, &rules)
	if rules.Digest == "" || len(rules.Layers) != 1 {
		t.Fatalf("pad_rules should report the store layer: %+v", rules)
	}

	if msg := callErr(t, cs, "pad_create", map[string]any{
		"project": "default", "author": "pm", "title": "kickoff", "content": "starting",
	}); !strings.Contains(msg, "rules_unread") || !strings.Contains(msg, rules.Digest) {
		t.Fatalf("create without an ack must fail with the rules and the digest: %s", msg)
	}

	var created createOutput
	call(t, cs, "pad_create", map[string]any{
		"project": "default", "author": "pm", "title": "kickoff", "content": "starting",
		"ack_rules": rules.Digest,
	}, &created)

	// Setting the pad's rules through pad_post: no turn taken, so pm may still not post
	// an ordinary message afterwards.
	call(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "pm", "title": "House style",
		"content": "- progress goes on the task", "set_rules": true,
	}, nil)
	if msg := callErr(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "pm", "title": "again", "content": "x",
	}); !strings.Contains(msg, "not_your_turn") {
		t.Fatalf("rules must not hand pm the turn: %s", msg)
	}

	var padRules rulesOutput
	call(t, cs, "pad_rules", map[string]any{"ref": created.Ref}, &padRules)
	if len(padRules.Layers) != 2 || padRules.Layers[1].Level != "pad" {
		t.Fatalf("the pad's own rules should be the second layer: %+v", padRules.Layers)
	}

	// A newcomer meets the rules on pad_get, before writing anything.
	var got getOutput
	call(t, cs, "pad_get", map[string]any{"ref": created.Ref, "author": "ios"}, &got)
	if got.Rules == nil || got.Rules.Digest != padRules.Digest {
		t.Fatalf("pad_get must hand a newcomer the rules: %+v", got.Rules)
	}
	if msg := callErr(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "ios", "title": "hi", "content": "hello",
	}); !strings.Contains(msg, "rules_unread") {
		t.Fatalf("a newcomer's first post must be gated: %s", msg)
	}
	call(t, cs, "pad_post", map[string]any{
		"ref": created.Ref, "author": "ios", "title": "hi", "content": "hello",
		"ack_rules": padRules.Digest,
	}, nil)

	// Once they are on the pad, pad_get stops repeating the rules at them. (A fresh
	// value to decode into: an absent field leaves the previous one in place.)
	var settled getOutput
	call(t, cs, "pad_get", map[string]any{"ref": created.Ref, "author": "ios"}, &settled)
	if settled.Rules != nil {
		t.Fatalf("the rules should not be repeated to an author already on the pad: %+v", settled.Rules)
	}
}

// The reserved identity is not reachable from the agent surface at all.
func TestSystemAuthorRefusedOverMCP(t *testing.T) {
	cs, _ := setup(t)
	if msg := callErr(t, cs, "pad_create", map[string]any{
		"author": "scratchpad", "title": "t", "content": "c",
	}); !strings.Contains(msg, "reserved") {
		t.Fatalf("want the reserved-name error, got: %s", msg)
	}
}
