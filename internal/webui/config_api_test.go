package webui

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// The marker these tests start from. It carries the two settings this surface must never
// write — the TCP bearer digests and the rules policy — so every case can check they came
// through untouched.
const testMarker = `{
  "type": "scratchpad",
  "version": 1,
  "display_name": "Test",
  "instance": "scratchpad",
  "limits": { "max_sections_per_pad": 10 },
  "tcp": { "port": 6710, "token_digests": ["sha256:secret"] },
  "ui": { "port": 6711 },
  "rules": { "store": "ui", "project": "ui", "pad": "opener" }
}`

// newConfigServer is newTestServer with a REAL marker on disk, which /api/config needs:
// it reads and writes the file rather than the in-memory config.
func newConfigServer(t *testing.T) (*Server, *httptest.Server, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.MarkerPath(dir), []byte(testMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(cfg)
	srv, err := New(store.New(live), live, Options{Port: config.DefaultUIPort})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	authenticate(t, srv, ts, client)
	return srv, ts, client, dir
}

func TestConfigGetSeparatesWrittenFromEffective(t *testing.T) {
	_, ts, client, dir := newConfigServer(t)

	var got configResponse
	if code := getJSON(t, client, ts.URL+"/api/config", &got); code != http.StatusOK {
		t.Fatalf("GET /api/config = %d", code)
	}

	// Written: only what the marker actually says. Effective: what it means. The form
	// needs both to leave an unset field empty and show the default as a placeholder.
	if got.Config.Limits.MaxSectionsPerPad != 10 {
		t.Errorf("written limit = %d, want 10", got.Config.Limits.MaxSectionsPerPad)
	}
	if got.Config.Limits.MaxContentKB != 0 {
		t.Errorf("an unset limit came back as %d, want 0 (unset)", got.Config.Limits.MaxContentKB)
	}
	if got.Effective.Limits.MaxContentKB != config.DefaultLimits.MaxContentKB {
		t.Errorf("effective content limit = %d, want the default", got.Effective.Limits.MaxContentKB)
	}
	if got.Defaults.Limits.MaxSectionsPerPad != config.DefaultLimits.MaxSectionsPerPad {
		t.Errorf("defaults not reported: %+v", got.Defaults.Limits)
	}
	if got.Digest == "" {
		t.Error("no digest to quote on a save")
	}
	if got.Cold.Instance != "scratchpad" || got.Cold.MarkerFile != config.MarkerPath(dir) {
		t.Errorf("cold block wrong: %+v", got.Cold)
	}
	if got.Cold.Rules.Store != config.RulesWriteUI {
		t.Errorf("rules policy not shown: %+v", got.Cold.Rules)
	}
}

// The response must not carry the TCP bearer digests. A secret does not become printable
// because the page showing it is read-only.
func TestConfigGetOmitsTokenDigests(t *testing.T) {
	_, ts, client, _ := newConfigServer(t)

	resp, err := client.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, needle := range []string{"secret", "token_digest"} {
		if strings.Contains(lower, needle) {
			t.Fatalf("the config response mentions %q: %s", needle, body)
		}
	}
}

func TestConfigPutSavesAndReloads(t *testing.T) {
	srv, ts, client, dir := newConfigServer(t)

	var before configResponse
	getJSON(t, client, ts.URL+"/api/config", &before)

	body := `{"config":{"display_name":"Renamed","default_project":"","limits":` +
		`{"max_sections_per_pad":5000},"wait":{}},"if_digest":"` + before.Digest + `"}`
	var after configResponse
	if code := putJSON(t, client, ts.URL+"/api/config", body, &after); code != http.StatusOK {
		t.Fatalf("PUT /api/config = %d", code)
	}
	if after.Config.Limits.MaxSectionsPerPad != 5000 || after.Config.DisplayName != "Renamed" {
		t.Fatalf("the response did not reflect the save: %+v", after.Config)
	}
	if after.Digest == before.Digest {
		t.Error("the digest did not move after a save")
	}

	// On disk, and still loadable — the file is the source of truth for every other
	// process using this store.
	onDisk, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Limits.MaxSectionsPerPad != 5000 {
		t.Errorf("limit on disk = %d", onDisk.Limits.MaxSectionsPerPad)
	}
	if len(onDisk.TCP.TokenDigests) != 1 || onDisk.TCP.TokenDigests[0] != "sha256:secret" {
		t.Errorf("a save through the UI dropped the tcp digests: %+v", onDisk.TCP)
	}
	if onDisk.Rules != config.DefaultRulesPolicy {
		t.Errorf("a save through the UI changed the rules policy: %+v", onDisk.Rules)
	}

	// The process that served the save has already adopted it — a person who pressed Save
	// must not then hit a limit that has not moved yet. (Other processes get it from the
	// marker watcher; that path has its own tests in internal/watch.)
	if got := srv.live.Get(); got.Limits.MaxSectionsPerPad != 5000 {
		t.Fatalf("the running config still says %d", got.Limits.MaxSectionsPerPad)
	}
	// …and adopting it did not drag a cold group along.
	if got := srv.live.Get(); got.ProjectsDir != filepath.Join(dir, "projects") {
		t.Fatalf("the store path moved on save: %q", got.ProjectsDir)
	}
}

// A save that quotes a version somebody else has already replaced must lose, and say so
// with the code the page branches on to re-read rather than overwrite.
func TestConfigPutStaleDigestConflicts(t *testing.T) {
	_, ts, client, dir := newConfigServer(t)

	var first configResponse
	getJSON(t, client, ts.URL+"/api/config", &first)

	body := `{"config":{"display_name":"one","limits":{},"wait":{}},"if_digest":"` + first.Digest + `"}`
	if code := putJSON(t, client, ts.URL+"/api/config", body, nil); code != http.StatusOK {
		t.Fatalf("first save = %d", code)
	}

	second := `{"config":{"display_name":"two","limits":{},"wait":{}},"if_digest":"` + first.Digest + `"}`
	resp := putRaw(t, client, ts.URL+"/api/config", second)
	if resp.status != http.StatusConflict || resp.code != config.CodeConfigStale {
		t.Fatalf("stale save = %d/%s, want 409/%s", resp.status, resp.code, config.CodeConfigStale)
	}

	onDisk, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.DisplayName != "one" {
		t.Fatalf("display name = %q, want the first writer's", onDisk.DisplayName)
	}
}

// The handler assigns the four editable groups itself, so a request naming anything else
// simply has nowhere to land. This is the test that keeps that true.
func TestConfigPutCannotReachGuardedGroups(t *testing.T) {
	_, ts, client, dir := newConfigServer(t)

	var cur configResponse
	getJSON(t, client, ts.URL+"/api/config", &cur)

	body := `{"config":{"display_name":"Test","limits":{},"wait":{},` +
		`"rules":{"store":"agent","project":"agent","pad":"any"},` +
		`"tcp":{"token_digests":["sha256:mine"]},"ui":{"no_auth":true},"instance":"stolen"},` +
		`"if_digest":"` + cur.Digest + `"}`
	if code := putJSON(t, client, ts.URL+"/api/config", body, nil); code != http.StatusOK {
		t.Fatalf("PUT = %d; the extra fields should be ignored, not rejected", code)
	}

	onDisk, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Rules != config.DefaultRulesPolicy {
		t.Fatalf("rules policy was writable through the UI: %+v", onDisk.Rules)
	}
	if onDisk.UI.NoAuth {
		t.Fatal("ui.no_auth was writable through the UI")
	}
	if onDisk.Instance != "scratchpad" {
		t.Fatalf("instance was writable through the UI: %q", onDisk.Instance)
	}
	if len(onDisk.TCP.TokenDigests) != 1 || onDisk.TCP.TokenDigests[0] != "sha256:secret" {
		t.Fatalf("tcp token digests were writable through the UI: %+v", onDisk.TCP)
	}
}

func TestConfigPutRejectsImpossibleValues(t *testing.T) {
	_, ts, client, dir := newConfigServer(t)

	var cur configResponse
	getJSON(t, client, ts.URL+"/api/config", &cur)

	body := `{"config":{"display_name":"Test","limits":{"max_content_kb":-5},"wait":{}},` +
		`"if_digest":"` + cur.Digest + `"}`
	if code := putJSON(t, client, ts.URL+"/api/config", body, nil); code != http.StatusBadRequest {
		t.Fatalf("PUT with a negative limit = %d, want 400", code)
	}
	if _, err := config.LoadDir(dir); err != nil {
		t.Fatalf("the refused write left an unloadable marker: %v", err)
	}
}

// A saved page must not become a way in without a session.
func TestConfigRequiresSession(t *testing.T) {
	_, ts, _, _ := newConfigServer(t)
	anon := &http.Client{Timeout: 10 * time.Second}
	if code := getJSON(t, anon, ts.URL+"/api/config", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/config = %d, want 401", code)
	}
}

// rawResult is a status plus the stable error code, for the cases that assert on both.
type rawResult struct {
	status int
	code   string
}

func putRaw(t *testing.T, client *http.Client, url, body string) rawResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return rawResult{status: resp.StatusCode, code: payload.Code}
}
