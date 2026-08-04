package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/pad"
)

// markerDir writes a marker with the given JSON body and returns the dir holding it.
func markerDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(MarkerPath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A marker carrying settings this surface must never touch, so every test can check they
// survived: the TCP bearer digests and the rules policy are the two that matter.
const guardedMarker = `{
  "type": "scratchpad",
  "version": 1,
  "display_name": "Old name",
  "instance": "prod",
  "limits": { "max_sections_per_pad": 10 },
  "tcp": { "port": 6710, "token_digests": ["sha256:secret"] },
  "ui": { "port": 6711, "no_auth": false },
  "rules": { "store": "ui", "project": "ui", "pad": "opener" }
}`

func TestUpdateMarkerWritesHotAndKeepsTheRest(t *testing.T) {
	dir := markerDir(t, guardedMarker)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}

	next, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.DisplayName = "New name"
		c.Limits.MaxSectionsPerPad = 5000
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.DisplayName != "New name" || next.Limits.MaxSectionsPerPad != 5000 {
		t.Fatalf("returned config did not carry the edit: %+v", next)
	}

	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal("the written marker no longer loads:", err)
	}
	if got.Limits.MaxSectionsPerPad != 5000 || got.DisplayName != "New name" {
		t.Errorf("edit did not reach the file: %+v", got)
	}
	if got.Instance != "prod" || got.UI.Port != 6711 || got.TCP.Port != 6710 {
		t.Errorf("cold groups were lost: %+v", got)
	}
	if len(got.TCP.TokenDigests) != 1 || got.TCP.TokenDigests[0] != "sha256:secret" {
		t.Errorf("tcp token digests were lost: %+v", got.TCP)
	}
	if got.Rules != (RulesPolicy{Store: RulesWriteUI, Project: RulesWriteUI, Pad: RulesWriteOpener}) {
		t.Errorf("rules policy was lost: %+v", got.Rules)
	}
	if fi, err := os.Stat(MarkerPath(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
}

// An unset field must stay unset. Round-tripping a defaulted config would write every
// built-in into the operator's file, and a deployment that never chose a value would stop
// following the default the day it changes.
func TestUpdateMarkerDoesNotMaterialiseDefaults(t *testing.T) {
	dir := markerDir(t, `{"type":"scratchpad","version":1,"instance":"prod"}`)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.Limits.MaxSectionsPerPad = 42
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	limits, _ := m["limits"].(map[string]any)
	if _, ok := limits["max_sections_per_pad"]; !ok {
		t.Fatalf("the edit is missing from the file: %s", raw)
	}
	if _, ok := limits["max_content_kb"]; ok {
		t.Errorf("an untouched limit was written out as an explicit setting: %s", raw)
	}
	if _, ok := m["wait"]; ok {
		t.Errorf("an untouched group was written out: %s", raw)
	}
	// Derived paths are computed from the dir at load time. Persisting them would turn a
	// self-contained directory into one that remembers where it used to live.
	for _, k := range []string{"root_dir", "projects_dir", "socket_path"} {
		if _, ok := m[k]; ok {
			t.Errorf("derived %s was persisted: %s", k, raw)
		}
	}
}

// A save edits the groups it names; it does not rewrite the file. Anything this binary
// does not model — a note the operator keeps there, a setting a newer build added — must
// survive, because the alternative is that pressing Save in a browser silently deletes
// part of the operator's configuration.
func TestUpdateMarkerKeepsKeysItDoesNotModel(t *testing.T) {
	dir := markerDir(t, `{
  "type": "scratchpad",
  "version": 1,
  "_comment": "raised for the migration, ask before lowering",
  "display_name": "Old name",
  "instance": "prod",
  "limits": { "max_sections_per_pad": 10, "max_content_kb": 64 },
  "tcp": { "port": 6710, "token_digests": ["sha256:secret"] },
  "ui": { "no_auth": false },
  "future_group": { "require_attestation": true }
}`)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.Limits.MaxSectionsPerPad = 5000
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("the merged marker is not valid JSON: %v\n%s", err, raw)
	}
	if m["_comment"] != "raised for the migration, ask before lowering" {
		t.Errorf("an unmodelled key was dropped: %s", raw)
	}
	fut, _ := m["future_group"].(map[string]any)
	if fut["require_attestation"] != true {
		t.Errorf("an unmodelled GROUP was dropped: %s", raw)
	}
	limits, _ := m["limits"].(map[string]any)
	if limits["max_sections_per_pad"] != float64(5000) || limits["max_content_kb"] != float64(64) {
		t.Errorf("the edit did not land, or took a sibling with it: %s", raw)
	}
	tcp, _ := m["tcp"].(map[string]any)
	digests, _ := tcp["token_digests"].([]any)
	if len(digests) != 1 || digests[0] != "sha256:secret" {
		t.Errorf("the tcp digests did not survive the merge: %s", raw)
	}
	// An explicit false is an assertion the operator wrote down. `absent` means the same
	// thing to the loader, but a save about LIMITS has no business deleting the line that
	// says this deployment is not running without auth.
	ui, ok := m["ui"].(map[string]any)
	if !ok || ui["no_auth"] != false {
		t.Errorf("an explicitly-written security assertion was dropped: %s", raw)
	}

	// Key order follows the file, so an edit is a small diff rather than a reshuffle.
	order, err := objectKeyOrder(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) < 2 || order[0] != "type" || order[1] != "version" {
		t.Errorf("key order was not preserved: %v", order)
	}
	if _, lerr := LoadDir(dir); lerr != nil {
		t.Fatalf("the merged marker no longer loads: %v", lerr)
	}
}

// Blanking a modelled field must still REMOVE it — "unset" has one spelling in the file,
// and the merge must not turn a cleared field into a resurrected old value.
func TestUpdateMarkerRemovesAClearedField(t *testing.T) {
	dir := markerDir(t, `{"type":"scratchpad","version":1,"default_project":"work",
		"limits":{"max_sections_per_pad":10}}`)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.DefaultProject = ""
		c.Limits = Limits{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["default_project"]; ok {
		t.Errorf("a cleared field lingered: %s", raw)
	}
	if _, ok := m["limits"]; ok {
		t.Errorf("a cleared group lingered: %s", raw)
	}
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProject != DefaultProject || !SameLimits(got.Limits, DefaultLimits) {
		t.Errorf("clearing a field must fall back to the defaults: %+v", got)
	}
}

func TestUpdateMarkerRefusesStaleOrMissingDigest(t *testing.T) {
	for _, tc := range []struct{ name, digest string }{
		{"blank", ""},
		{"stale", "0badcafe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := markerDir(t, guardedMarker)
			before, err := os.ReadFile(MarkerPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			_, err = UpdateMarker(dir, tc.digest, OperatorEditable, func(c *Config) error {
				c.DisplayName = "clobbered"
				return nil
			})
			if !pad.HasCode(err, CodeConfigStale) {
				t.Fatalf("err = %v, want %s", err, CodeConfigStale)
			}
			after, err := os.ReadFile(MarkerPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("a refused write still touched the file")
			}
		})
	}
}

// The digest is over the RAW bytes, so a change this binary does not model still counts.
func TestMarkerDigestCoversUnknownFields(t *testing.T) {
	a := MarkerDigest([]byte(`{"type":"scratchpad","version":1}`))
	b := MarkerDigest([]byte(`{"type":"scratchpad","version":1,"something_new":true}`))
	if a == b {
		t.Fatal("digest ignored a field the struct does not know about")
	}
	if len(a) != DigestLen {
		t.Fatalf("digest length = %d, want %d", len(a), DigestLen)
	}
}

func TestUpdateMarkerRefusesColdGroups(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"instance", func(c *Config) { c.Instance = "other" }},
		{"ui port", func(c *Config) { c.UI.Port = 9999 }},
		{"ui no_auth", func(c *Config) { c.UI.NoAuth = true }},
		{"tcp tokens", func(c *Config) { c.TCP.TokenDigests = append(c.TCP.TokenDigests, "sha256:mine") }},
		{"dir pointer", func(c *Config) { c.Dir = "/elsewhere" }},
		{"schema version", func(c *Config) { c.Version = 99 }},
		// The one that matters most, and the one a caller is likeliest to reach by
		// accident: rules RELOADS without a restart, so nothing in the hot/cold split
		// stops it. Only being absent from the allowed set does.
		{"rules store", func(c *Config) { c.Rules.Store = RulesWriteAgent }},
		{"rules pad", func(c *Config) { c.Rules.Pad = RulesWriteAny }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := markerDir(t, guardedMarker)
			_, digest, err := ReadMarker(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
				tc.edit(c)
				return nil
			}); !pad.HasCode(err, CodeConfigReadOnly) {
				t.Fatalf("err = %v, want %s", err, CodeConfigReadOnly)
			}
		})
	}
}

// A caller that IS allowed to write the rules may — the refusal above is about the
// allowed set, not about rules being unwritable in principle. Without this, the test
// above would still pass if UpdateMarker simply hardcoded a refusal.
func TestUpdateMarkerWritesRulesWhenAllowed(t *testing.T) {
	dir := markerDir(t, guardedMarker)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	allowed := append(slices.Clone(OperatorEditable), GroupRules)
	if _, err := UpdateMarker(dir, digest, allowed, func(c *Config) error {
		c.Rules.Store = RulesWriteAgent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rules.Store != RulesWriteAgent {
		t.Fatalf("rules.store = %q", got.Rules.Store)
	}
}

// A value the loader would reject must never reach the file: a marker that cannot be
// loaded takes every command down with it, including the one used to fix it.
func TestUpdateMarkerRefusesUnloadableValues(t *testing.T) {
	rulesToo := append(slices.Clone(OperatorEditable), GroupRules)
	for _, tc := range []struct {
		name    string
		allowed []string
		edit    func(*Config)
		want    string
	}{
		{"negative limit", OperatorEditable, func(c *Config) { c.Limits.MaxContentKB = -1 }, "cannot be negative"},
		{"absurd limit", OperatorEditable, func(c *Config) { c.Limits.MaxSectionsPerPad = 1 << 40 }, "at most"},
		{"wait default over max", OperatorEditable, func(c *Config) { c.Wait = Wait{DefaultS: 300, MaxS: 60} }, "cannot exceed"},
		// A project name the store could never use. Without this the marker saves cleanly
		// and every `pad create` afterwards fails with invalid_project_name instead.
		{"bad default project", OperatorEditable, func(c *Config) { c.DefaultProject = "My Project" }, "only a-z and 0-9"},
		{"traversal-shaped project", OperatorEditable, func(c *Config) { c.DefaultProject = "../../etc" }, "only a-z and 0-9"},
		{"multiline display name", OperatorEditable, func(c *Config) { c.DisplayName = "a\nb" }, "single line"},
		// Reachable only by a caller allowed to write rules at all — the loader's own
		// check, restated here so a bad policy cannot be written by whoever may.
		{"unknown rules policy", rulesToo, func(c *Config) { c.Rules.Store = "everyone" }, "not one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := markerDir(t, guardedMarker)
			_, digest, err := ReadMarker(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = UpdateMarker(dir, digest, tc.allowed, func(c *Config) error {
				tc.edit(c)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if _, lerr := LoadDir(dir); lerr != nil {
				t.Fatalf("the refused write left an unloadable marker: %v", lerr)
			}
		})
	}
}

// Two saves from the same read: the second must lose, not silently overwrite the first.
func TestUpdateMarkerSecondWriterLoses(t *testing.T) {
	dir := markerDir(t, guardedMarker)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.DisplayName = "first"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.DisplayName = "second"
		return nil
	}); !pad.HasCode(err, CodeConfigStale) {
		t.Fatalf("err = %v, want %s", err, CodeConfigStale)
	}
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "first" {
		t.Fatalf("display name = %q, want the first writer's", got.DisplayName)
	}
}

// The temp file the atomic write goes through must not be left behind — it would sit in
// the Scratchpad dir looking like part of the store.
func TestUpdateMarkerLeavesNoTempFile(t *testing.T) {
	dir := markerDir(t, guardedMarker)
	_, digest, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMarker(dir, digest, OperatorEditable, func(c *Config) error {
		c.DisplayName = "x"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}
