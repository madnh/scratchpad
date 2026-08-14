package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
)

func padGetTestStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := config.WriteMarker(dir, config.Config{}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, store.New(config.NewLive(cfg))
}

func runPadGet(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newPadGetCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append(args, "--dir", dir))
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func TestPadGetJSONCarriesStructuredTurnAndMetadata(t *testing.T) {
	dir, st := padGetTestStore(t)
	p, _, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Retry budget", Content: "starting\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Post(store.PostRequest{
		Ref: p.Ref(), Author: "pm", Title: "Implement retry budget", Content: "assigned\n",
		Meta:     pad.Meta{Kind: pad.KindTask, To: []string{"backend"}, Status: pad.StatusOpen},
		OpenTask: true,
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runPadGet(t, dir, p.Ref(), "--json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var got padGetReport
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("pad get --json must emit one document, second decode = %v", err)
	}

	if got.Ref != p.Ref() || got.Project != "projectx" || got.SectionCount != 2 || got.Protected {
		t.Fatalf("wrong pad metadata: %+v", got)
	}
	if !reflect.DeepEqual(got.Authors, []string{"frontend", "pm"}) {
		t.Fatalf("authors = %v", got.Authors)
	}
	if got.Turn.LastAuthor != "frontend" || !reflect.DeepEqual(got.Turn.Blocked, []string{"frontend"}) {
		t.Fatalf("turn must follow the last message, not the later task event: %+v", got.Turn)
	}
	if len(got.Sections) != 2 || got.Sections[0].Content != "" || got.Sections[1].Content != "" {
		t.Fatalf("sections must be a body-free table of contents: %+v", got.Sections)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ref", "project", "created_ts", "section_count", "authors", "protected", "turn", "sections"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON is missing %q", key)
		}
	}
	var turn map[string]json.RawMessage
	if err := json.Unmarshal(raw["turn"], &turn); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"last_author", "blocked", "waiting_for"} {
		if _, ok := turn[key]; !ok {
			t.Errorf("turn is missing %q", key)
		}
	}
}

func TestPadGetJSONKeepsProtectedPadGate(t *testing.T) {
	dir, st := padGetTestStore(t)
	p, password, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Private", Content: "secret\n", Protect: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = runPadGet(t, dir, p.Ref(), "--json")
	if err == nil || !store.HasCode(err, store.CodeUnauthorized) {
		t.Fatalf("without password: err = %v", err)
	}

	stdout, _, err := runPadGet(t, dir, p.Ref(), "--json", "--password", password)
	if err != nil {
		t.Fatal(err)
	}
	var got padGetReport
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Protected {
		t.Fatal("authorized JSON must still report protected=true")
	}
}

func TestPadGetTextDefaultMatchesExplicitJSONFalse(t *testing.T) {
	dir, st := padGetTestStore(t)
	p, _, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Retry budget", Content: "starting\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	want, _, err := runPadGet(t, dir, p.Ref())
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := runPadGet(t, dir, p.Ref(), "--json=false")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("text output changed when JSON is disabled\nwant:\n%s\ngot:\n%s", want, got)
	}
}
