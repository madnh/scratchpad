package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
)

// digestOf is how an operator produces a marker entry from a token they just generated.
func digestOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func liveWith(clients ...config.AuthClient) *config.Live {
	return config.NewLive(config.Config{Auth: config.Auth{Clients: clients}})
}

// ask sends one request through the guard and reports the status it produced.
func ask(t *testing.T, h http.Handler, token string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:6710/mcp", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestTCPGuardAcceptsAConfiguredCredential(t *testing.T) {
	live := liveWith(config.AuthClient{Name: "laptop", Digest: digestOf("tok-a")})
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, h, "tok-a"); got != http.StatusOK {
		t.Errorf("the configured token was refused: %d", got)
	}
	if got := ask(t, h, "tok-wrong"); got != http.StatusUnauthorized {
		t.Errorf("an unknown token was accepted: %d", got)
	}
	if got := ask(t, h, ""); got != http.StatusUnauthorized {
		t.Errorf("no token was accepted: %d", got)
	}
}

// TestTCPGuardRevocationNeedsNoRestart is the reason this group exists and the reason the
// guard reads `*config.Live` per request rather than baking a map into its closure.
//
// Revoking used to require a restart, and a restart severs every agent parked in
// `pad wait` on this deployment — so the emergency operation was the most expensive one
// available, which is the kind of cost that gets a revocation postponed.
//
// It goes through live.Apply, not live.Set, deliberately: Apply is the reload path, so
// this also pins `auth` as a HOT group. Were it ever reclassified as cold, MergeHot would
// keep the old credentials and this test would fail rather than the deployment quietly
// continuing to honour a token somebody thought they had revoked.
func TestTCPGuardRevocationNeedsNoRestart(t *testing.T) {
	keep := config.AuthClient{Name: "ci", Digest: digestOf("tok-ci")}
	revoke := config.AuthClient{Name: "laptop", Digest: digestOf("tok-laptop")}
	live := liveWith(keep, revoke)
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, h, "tok-laptop"); got != http.StatusOK {
		t.Fatalf("precondition: the laptop token should work, got %d", got)
	}

	// The operator removes exactly one named client — which is what naming them is for.
	live.Apply(config.Config{Auth: config.Auth{Clients: []config.AuthClient{keep}}})

	if got := ask(t, h, "tok-laptop"); got != http.StatusUnauthorized {
		t.Errorf("the revoked token still works without a restart: %d", got)
	}
	if got := ask(t, h, "tok-ci"); got != http.StatusOK {
		t.Errorf("revoking one credential took another down with it: %d", got)
	}
}

func TestTCPGuardPicksUpANewCredentialWithoutRestart(t *testing.T) {
	live := liveWith(config.AuthClient{Name: "ci", Digest: digestOf("tok-ci")})
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, h, "tok-new"); got != http.StatusUnauthorized {
		t.Fatalf("precondition: tok-new is not configured yet, got %d", got)
	}
	live.Apply(config.Config{Auth: config.Auth{Clients: []config.AuthClient{
		{Name: "ci", Digest: digestOf("tok-ci")},
		{Name: "second-laptop", Digest: digestOf("tok-new")},
	}}})
	if got := ask(t, h, "tok-new"); got != http.StatusOK {
		t.Errorf("a credential added to the marker did not take effect: %d", got)
	}
}

// TestTCPGuardEmptyCredentialsDenyEverything pins the direction of the failure. A marker
// edited down to no clients must lock the door, never open it — "no credentials
// configured" and "no credential required" must never be the same state.
func TestTCPGuardEmptyCredentialsDenyEverything(t *testing.T) {
	live := liveWith(config.AuthClient{Name: "laptop", Digest: digestOf("tok-a")})
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	live.Apply(config.Config{})
	for _, tok := range []string{"tok-a", "", "anything"} {
		if got := ask(t, h, tok); got != http.StatusUnauthorized {
			t.Errorf("token %q got through an empty credential list: %d", tok, got)
		}
	}
}

// TestTCPGuardFlagOverrideIgnoresTheMarker is the config model showing through: a flag
// beats the marker, and a flag is a process argument. An operator who pinned credentials
// on the command line must not have them widened by someone editing a file.
func TestTCPGuardFlagOverrideIgnoresTheMarker(t *testing.T) {
	live := liveWith(config.AuthClient{Name: "marker", Digest: digestOf("tok-marker")})
	h, err := tcpGuard(TCPOptions{
		Realm:         "test",
		TokenOverride: []string{digestOf("tok-flag")},
	}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, h, "tok-flag"); got != http.StatusOK {
		t.Errorf("the flag's own token was refused: %d", got)
	}
	if got := ask(t, h, "tok-marker"); got != http.StatusUnauthorized {
		t.Errorf("the marker was consulted despite an explicit override: %d", got)
	}
	// And it stays pinned: a marker edit cannot add to it.
	live.Apply(config.Config{Auth: config.Auth{Clients: []config.AuthClient{
		{Name: "sneaky", Digest: digestOf("tok-sneaky")},
	}}})
	if got := ask(t, h, "tok-sneaky"); got != http.StatusUnauthorized {
		t.Errorf("a marker edit widened a command-line override: %d", got)
	}
}

// TestTCPGuardStillAcceptsLegacyTokenDigests: the anonymous array is the operator's
// existing configuration, and a release must not lock anybody out of their own
// deployment.
func TestTCPGuardStillAcceptsLegacyTokenDigests(t *testing.T) {
	live := config.NewLive(config.Config{
		TCP: config.TCP{TokenDigests: []string{digestOf("tok-legacy")}},
	})
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	if got := ask(t, h, "tok-legacy"); got != http.StatusOK {
		t.Errorf("a legacy tcp.token_digests entry was refused: %d", got)
	}
}

// TestTCPGuardRejectsNonLoopbackHost covers the DNS-rebinding guard that was already
// here: a browser resolving evil.example to 127.0.0.1 still sends its own Host.
func TestTCPGuardRejectsNonLoopbackHost(t *testing.T) {
	live := liveWith(config.AuthClient{Name: "laptop", Digest: digestOf("tok-a")})
	h, err := tcpGuard(TCPOptions{Realm: "test"}, live, okHandler())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://evil.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer tok-a")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a non-loopback Host got past the guard: %d", w.Code)
	}
}

func TestTCPGuardRejectsAMalformedOverride(t *testing.T) {
	live := liveWith()
	if _, err := tcpGuard(TCPOptions{TokenOverride: []string{"sha256:nope"}}, live, okHandler()); err == nil {
		t.Fatal("a malformed --tcp-token-digest was accepted")
	}
}
