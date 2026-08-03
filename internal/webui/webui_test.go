package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
	"github.com/madnh/scratchpad/internal/watch"
)

// newTestServer builds a UI server over a fresh store plus an httptest listener, and
// returns a client that has already redeemed the one-time token.
func newTestServer(t *testing.T) (*Server, *httptest.Server, *http.Client, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Type: config.ConfigType, Version: config.ConfigVersion,
		DisplayName: "Test", Instance: "scratchpad",
		ProjectsDir: projects, Limits: config.DefaultLimits,
	}
	st := store.New(projects, config.DefaultLimits)

	srv, err := New(st, cfg, Options{Port: config.DefaultUIPort})
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
	return srv, ts, client, st
}

// watchEvent builds the watcher event the hub would receive for a changed pad.
func watchEvent(ref, project string) watch.Event {
	return watch.Event{Ref: ref, Project: project}
}

// authenticate redeems the one-time token so the client holds a session cookie.
func authenticate(t *testing.T, srv *Server, ts *httptest.Server, client *http.Client) {
	t.Helper()
	resp, err := client.Get(ts.URL + "/?t=" + url.QueryEscape(srv.auth.token))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redeeming the token gave %d, want 200 after the redirect", resp.StatusCode)
	}
}

// getJSON performs an authenticated GET and decodes the body.
func getJSON(t *testing.T, client *http.Client, url string, into any) int {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestAPIRequiresSession(t *testing.T) {
	_, ts, client, _ := newTestServer(t)
	if code := getJSON(t, client, ts.URL+"/api/pads", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/pads gave %d, want 401", code)
	}
}

func TestTokenRedemptionDropsTokenFromURL(t *testing.T) {
	srv, ts, client, _ := newTestServer(t)

	// Do not follow the redirect, so the Location header can be inspected.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(ts.URL + "/?t=" + srv.auth.token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("token redemption gave %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "t=") {
		t.Fatalf("the redirect still carries the token: %q", loc)
	}
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			found = true
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie must be HttpOnly + SameSite=Strict, got %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("no session cookie was set")
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	_, ts, client, _ := newTestServer(t)
	resp, err := client.Get(ts.URL + "/?t=not-the-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong token gave %d, want 401", resp.StatusCode)
	}
}

// A page that resolves its own hostname to 127.0.0.1 still sends its own Host — the
// guard that stops DNS rebinding.
func TestNonLoopbackHostRejected(t *testing.T) {
	srv, ts, client, _ := newTestServer(t)
	authenticate(t, srv, ts, client)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a non-loopback Host gave %d, want 403", resp.StatusCode)
	}
}

func TestCrossOriginWriteRejected(t *testing.T) {
	srv, ts, client, st := newTestServer(t)
	authenticate(t, srv, ts, client)
	pad, _, err := st.CreatePad("demo", "alice", "hello", "body", false)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/pads/"+pad.Ref(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin DELETE gave %d, want 403", resp.StatusCode)
	}
	if _, err := st.Get(pad.Ref(), ""); err != nil {
		t.Fatalf("the pad should still exist: %v", err)
	}
}

// The pad view must stay cheap on a long pad: the TOC carries no bodies, and section
// bodies arrive one page at a time, newest first.
func TestSectionsArePagedNewestFirst(t *testing.T) {
	srv, ts, client, st := newTestServer(t)
	authenticate(t, srv, ts, client)

	pad, _, err := st.CreatePad("demo", "alice", "s1", "body 1", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := pad.Ref()
	for n := 2; n <= 30; n++ {
		author := "alice"
		if n%2 == 0 {
			author = "bob"
		}
		if _, err := st.Post(ref, author, fmt.Sprintf("s%d", n), fmt.Sprintf("body %d", n), ""); err != nil {
			t.Fatal(err)
		}
	}

	var toc padResponse
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref, &toc); code != http.StatusOK {
		t.Fatalf("GET pad gave %d", code)
	}
	if toc.SectionCount != 30 || len(toc.Sections) != 30 {
		t.Fatalf("TOC has %d entries for %d sections, want 30/30", len(toc.Sections), toc.SectionCount)
	}
	if toc.Turn == nil || toc.Turn.LastAuthor != "bob" {
		t.Fatalf("turn state missing or wrong: %+v", toc.Turn)
	}
	// The roster the author filter is built from: each agent once, opener first.
	if len(toc.Authors) != 2 || toc.Authors[0] != "alice" || toc.Authors[1] != "bob" {
		t.Fatalf("authors %v, want [alice bob]", toc.Authors)
	}

	var page struct {
		Sections []store.Section `json:"sections"`
		HasOlder bool            `json:"has_older"`
		Total    int             `json:"total"`
	}
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections", &page); code != http.StatusOK {
		t.Fatalf("GET sections gave %d", code)
	}
	if len(page.Sections) != defaultSectionLimit {
		t.Fatalf("default page has %d sections, want %d", len(page.Sections), defaultSectionLimit)
	}
	if first, last := page.Sections[0].N, page.Sections[len(page.Sections)-1].N; first != 11 || last != 30 {
		t.Fatalf("default page covers #%d..#%d, want #11..#30 (the newest)", first, last)
	}
	if !page.HasOlder || page.Total != 30 {
		t.Fatalf("has_older=%v total=%d, want true/30", page.HasOlder, page.Total)
	}

	// Walking backwards from the oldest section of the first page.
	var older struct {
		Sections []store.Section `json:"sections"`
		HasOlder bool            `json:"has_older"`
	}
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections?before=11", &older); code != http.StatusOK {
		t.Fatalf("GET older sections gave %d", code)
	}
	if first, last := older.Sections[0].N, older.Sections[len(older.Sections)-1].N; first != 1 || last != 10 {
		t.Fatalf("older page covers #%d..#%d, want #1..#10", first, last)
	}
	if older.HasOlder {
		t.Fatal("has_older should be false at the start of the pad")
	}
}

func TestProtectedPadLocksContentNotIdentity(t *testing.T) {
	srv, ts, client, st := newTestServer(t)
	authenticate(t, srv, ts, client)

	pad, password, err := st.CreatePad("demo", "alice", "secret talk", "body", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := pad.Ref()

	var view padResponse
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref, &view); code != http.StatusOK {
		t.Fatalf("GET a locked pad gave %d, want 200 with locked=true", code)
	}
	if !view.Locked || !view.Protected {
		t.Fatalf("want locked+protected, got %+v", view)
	}
	if len(view.Sections) != 0 {
		t.Fatal("a locked pad must not expose its table of contents")
	}
	if view.Title != "secret talk" {
		t.Fatalf("listing-level metadata should still come through, got title %q", view.Title)
	}
	// The roster is listing-level too — the pads table shows it for this pad already.
	if len(view.Authors) != 1 || view.Authors[0] != "alice" {
		t.Fatalf("locked pad should still name its authors, got %v", view.Authors)
	}
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections", nil); code != http.StatusForbidden {
		t.Fatalf("sections of a locked pad gave %d, want 403", code)
	}

	body, _ := json.Marshal(map[string]string{"password": password})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pads/"+ref+"/unlock", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock gave %d, want 200", resp.StatusCode)
	}
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections", nil); code != http.StatusOK {
		t.Fatalf("sections after unlock gave %d, want 200", code)
	}
}

// The notification for a protected pad must say no more than its listing entry does.
func TestEventForProtectedPadHidesSectionTitle(t *testing.T) {
	srv, _, _, st := newTestServer(t)

	open, _, err := st.CreatePad("demo", "alice", "open pad", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	locked, _, err := st.CreatePad("demo", "alice", "locked pad", "body", true)
	if err != nil {
		t.Fatal(err)
	}

	ev := srv.hub.enrich(watchEvent(open.Ref(), "demo"))
	if ev.LastTitle != "open pad" {
		t.Fatalf("open pad event should carry the section title, got %q", ev.LastTitle)
	}
	ev = srv.hub.enrich(watchEvent(locked.Ref(), "demo"))
	if ev.LastTitle != "" {
		t.Fatalf("protected pad event leaked the section title %q", ev.LastTitle)
	}
	if ev.Title != "locked pad" || !ev.Protected {
		t.Fatalf("protected pad event should still carry listing metadata, got %+v", ev)
	}
}

// End to end: a post through the store reaches a connected browser as an SSE event,
// with no polling anywhere in between.
func TestSSEDeliversPadChange(t *testing.T) {
	srv, ts, client, st := newTestServer(t)
	authenticate(t, srv, ts, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.watcher.Run(ctx) }()
	go srv.hub.run(ctx)

	pad, _, err := st.CreatePad("demo", "alice", "hello", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // let the watcher seed and start

	resp, err := client.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %q, want text/event-stream", ct)
	}

	if _, err := st.Post(pad.Ref(), "bob", "reply", "the answer", ""); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev padEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", line, err)
		}
		if ev.Ref != pad.Ref() || ev.Type != "changed" {
			t.Fatalf("got %+v, want a change on %s", ev, pad.Ref())
		}
		if ev.LastAuthor != "bob" || ev.SectionCount != 2 {
			t.Fatalf("event should describe the new section, got %+v", ev)
		}
		return
	}
	t.Fatal("no SSE event arrived for a new section")
}

// newNoAuthServer builds a UI server with --no-auth, the mode where every request
// arrives without a cookie.
func newNoAuthServer(t *testing.T) (*Server, *httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Type: config.ConfigType, Version: config.ConfigVersion,
		DisplayName: "Test", Instance: "scratchpad",
		ProjectsDir: projects, Limits: config.DefaultLimits,
	}
	st := store.New(projects, config.DefaultLimits)
	srv, err := New(st, cfg, Options{Port: config.DefaultUIPort, NoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return srv, ts, st
}

// TestNoAuthServesCookielessRequests is the regression test for the session panic:
// under --no-auth the middleware mints a session and forwards the SAME request, which
// still carries no cookie. Deriving the session from the request a second time handed
// the handler a nil *session and every pad read crashed the connection. A client with
// no cookie jar reproduces it exactly.
func TestNoAuthServesCookielessRequests(t *testing.T) {
	_, ts, st := newNoAuthServer(t)
	pad, _, err := st.CreatePad("p", "alice", "hello", "body\n", false)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second} // deliberately NO cookie jar

	for _, path := range []string{
		"/api/pads",
		"/api/pads/" + pad.Ref(),
		"/api/pads/" + pad.Ref() + "/sections",
	} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s failed outright (the panic drops the connection): %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200; body: %s", path, resp.StatusCode, body)
		}
	}
}

// TestNoAuthUnlockDoesNotPanic covers the same shape on the unlock path, which ran the
// bcrypt compare before dereferencing the session.
func TestNoAuthUnlockDoesNotPanic(t *testing.T) {
	_, ts, st := newNoAuthServer(t)
	pad, password, err := st.CreatePad("p", "alice", "secret", "body\n", true)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}

	body := strings.NewReader(`{"password":"` + password + `"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pads/"+pad.Ref()+"/unlock", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unlock failed outright: %v", err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock = %d, want 200; body: %s", resp.StatusCode, out)
	}
}

// TestSessionTableIsBounded covers the other half of the same defect: a client that
// never returns the cookie minted a session per request, forever.
func TestSessionTableIsBounded(t *testing.T) {
	srv, ts, _ := newNoAuthServer(t)
	client := &http.Client{Timeout: 10 * time.Second} // no cookie jar: every request is new
	for i := 0; i < maxSessions*2; i++ {
		resp, err := client.Get(ts.URL + "/api/pads")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	srv.auth.mu.Lock()
	n := len(srv.auth.sessions)
	srv.auth.mu.Unlock()
	if n > maxSessions {
		t.Fatalf("session table holds %d entries, want at most %d", n, maxSessions)
	}
}

// TestSectionPreviewSkipsMarkdownFurniture pins what the outline's hover popup shows.
// A section written by an agent usually opens with a heading, a fence or a bullet, and
// none of those say anything about the section — the preview has to reach the prose.
func TestSectionPreviewSkipsMarkdownFurniture(t *testing.T) {
	cases := []struct {
		name, content, title, want string
	}{
		{"plain", "the first line\nthe second", "", "the first line\nthe second"},
		{"leading blanks", "\n\n  real content\n", "", "real content"},
		{"heading", "## Heading\nprose below", "", "Heading\nprose below"},
		{"fence", "```go\nfunc main() {}\n```\nafter", "", "func main() {}\nafter"},
		{"bullets", "- one\n- two", "", "one\ntwo"},
		{"rule only", "---\n***\ncontent", "", "content"},
		{"empty", "", "", ""},
		{"whitespace only", "   \n\t\n", "", ""},
		// Agents routinely open a section by repeating its title as a heading. The
		// popup already shows the title, so the excerpt starts below it.
		{"title repeated", "## Rate limits\nfive per minute", "Rate limits", "five per minute"},
		{"title later is content", "first\nRate limits", "Rate limits", "first\nRate limits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sectionPreview(c.content, c.title); got != c.want {
				t.Fatalf("sectionPreview(%q) = %q, want %q", c.content, got, c.want)
			}
		})
	}

	// The cut is made on RUNES: a preview that split a multi-byte character would put
	// a replacement glyph in the popup.
	long := strings.Repeat("á", previewChars*2)
	got := sectionPreview(long, "")
	if r := []rune(got); len(r) != previewChars || r[len(r)-1] != '…' {
		t.Fatalf("long preview is %d runes ending %q, want %d ending in an ellipsis", len(r), string(r[len(r)-1]), previewChars)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("preview is not valid UTF-8: %q", got)
	}
}

// TestSectionPreviewEndpoint covers the route the outline calls on hover: it answers
// for a real section, refuses one that does not exist, and — the part that matters —
// stays behind the same lock as the content it excerpts.
func TestSectionPreviewEndpoint(t *testing.T) {
	srv, ts, client, st := newTestServer(t)
	authenticate(t, srv, ts, client)

	pad, _, err := st.CreatePad("demo", "alice", "s1", "## Heading\nthe opening prose", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := pad.Ref()

	var got struct {
		N       int    `json:"n"`
		Author  string `json:"author"`
		Title   string `json:"title"`
		Preview string `json:"preview"`
	}
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections/1/preview", &got); code != http.StatusOK {
		t.Fatalf("GET preview gave %d", code)
	}
	if got.N != 1 || got.Author != "alice" || got.Title != "s1" {
		t.Fatalf("preview identifies the wrong section: %+v", got)
	}
	if got.Preview != "Heading\nthe opening prose" {
		t.Fatalf("preview = %q, want the prose without the heading marks", got.Preview)
	}

	var missing map[string]any
	if code := getJSON(t, client, ts.URL+"/api/pads/"+ref+"/sections/99/preview", &missing); code != http.StatusBadRequest {
		t.Fatalf("preview of a nonexistent section gave %d, want 400", code)
	}

	// A protected pad the session has not unlocked must not leak an excerpt.
	locked, _, err := st.CreatePad("demo", "alice", "secret talk", "the secret itself", true)
	if err != nil {
		t.Fatal(err)
	}
	var denied map[string]any
	if code := getJSON(t, client, ts.URL+"/api/pads/"+locked.Ref()+"/sections/1/preview", &denied); code != http.StatusForbidden {
		t.Fatalf("preview of a locked pad gave %d, want 403", code)
	}
}
