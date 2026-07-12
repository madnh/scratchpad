package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
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
	st := store.New(t.TempDir(), cfg.Limits)

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
	want := map[string]bool{
		"pad_create": false, "pad_post": false, "pad_get": false, "pad_read": false,
		"pad_wait": false, "pad_list": false, "project_list": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool %q (the surface is exactly the 7 agreed tools)", tool.Name)
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
		_, _ = st.Post(created.Ref, "b", "reply", "answer", "")
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
