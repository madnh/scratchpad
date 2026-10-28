package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/relay"
	"github.com/madnh/scratchpad/internal/store"
)

// The command path end to end: an agent under a relay runs `pad get --as` against a
// DIFFERENT store. Nothing is registered, and the agent is told to arm its own wait —
// while the same command against the relay's own store registers as before.
func TestPadGetAgainstAnotherStoreDoesNotRegister(t *testing.T) {
	mkStore := func() (string, *config.Live) {
		dir, err := os.MkdirTemp("", "rj")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		if err := config.WriteMarker(dir, config.Config{}); err != nil {
			t.Fatal(err)
		}
		cfg, _, _, err := config.Resolve(dir)
		if err != nil {
			t.Fatal(err)
		}
		return dir, config.NewLive(cfg)
	}
	homeDir, home := mkStore()
	otherDir, other := mkStore()

	srv := relay.NewServer(relay.SocketPath(home.Get().RootDir), home, func(string) error { return nil })
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	t.Setenv(relay.EnvSocket, srv.Socket())

	foreign, _, err := store.New(other).CreatePad(store.CreateRequest{Project: "alpha", Author: "ping", Title: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runPadGet(t, otherDir, foreign.Ref(), "--as", "ping")
	if err != nil {
		t.Fatal(err)
	}
	st, err := relay.Query(srv.Socket())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Watching) != 0 {
		t.Fatalf("a command against another store registered with this relay: %+v", st.Watching)
	}
	if strings.Contains(stdout, "relay: watching") || !strings.Contains(stderr, "arm a wait yourself") {
		t.Fatalf("the agent must be told nothing is listening:\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	own, _, err := store.New(home).CreatePad(store.CreateRequest{Project: "alpha", Author: "ping", Title: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if stdout, _, err = runPadGet(t, homeDir, own.Ref(), "--as", "ping"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "relay: watching "+own.Ref()) {
		t.Fatalf("the relay's own store must still register: %s", stdout)
	}
}

// The command end to end: only `pad read --since N` tells the relay what was read, and it
// reports the last section PRINTED. A plain `pad read` — a look at the whole pad, not an
// answer to a nudge — leaves the hold where it was.
func TestPadReadSinceLiftsTheHoldAndPlainReadDoesNot(t *testing.T) {
	dir, err := os.MkdirTemp("", "rd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := config.WriteMarker(dir, config.Config{}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(cfg)
	st := store.New(live)

	// Before the relay starts: a project dir created under a running watcher is picked up
	// on its own schedule, and a post right after it can land before that.
	p, _, err := st.CreatePad(store.CreateRequest{Project: "alpha", Author: "pong", Title: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}

	nudged := make(chan string, 8)
	srv := relay.NewServer(relay.SocketPath(cfg.RootDir), live, func(l string) error { nudged <- l; return nil })
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	t.Setenv(relay.EnvSocket, srv.Socket())

	if err := relay.Register(srv.Socket(), relay.Registration{Ref: p.Ref(), Author: "pong", Since: 1, Wake: relayWake}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Post(store.PostRequest{Ref: p.Ref(), Author: "ping", Title: "q", Content: "y"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-nudged:
	case <-time.After(5 * time.Second):
		t.Fatal("no first nudge")
	}
	watched := func() relay.Watched {
		t.Helper()
		s, err := relay.Query(srv.Socket())
		if err != nil || len(s.Watching) != 1 {
			t.Fatalf("status: %+v, %v", s, err)
		}
		return s.Watching[0]
	}

	read := func(args ...string) {
		t.Helper()
		cmd := newPadReadCmd()
		var out, errb bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		cmd.SetArgs(append([]string{p.Ref(), "--dir", dir}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "relay") || errb.Len() != 0 {
			t.Fatalf("a read must add nothing to its output:\nstdout=%s\nstderr=%s", out.String(), errb.String())
		}
	}

	read()
	if w := watched(); w.Since != 1 || w.NudgedTS == 0 {
		t.Fatalf("plain pad read moved the mark or lifted the hold: %+v", w)
	}
	read("--since", "1")
	if w := watched(); w.Since != 2 || w.NudgedTS != 0 {
		t.Fatalf("pad read --since 1 printed §2 and must record it: %+v", w)
	}
}
