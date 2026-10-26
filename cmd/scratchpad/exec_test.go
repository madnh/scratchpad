package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/relay"
	"github.com/madnh/scratchpad/internal/store"
)

// A rejoin must not double what an already-running session hears. Two terminals under one
// name is the duplicated agent: the relay that got there first keeps the pad, and the
// newcomer leaves it out — and says so on the summary line, not only in a warning below.
func TestPlanRejoinLeavesOutPadsAnotherRelayWatchesUnderTheSameName(t *testing.T) {
	// NOT t.TempDir(): a unix socket path is capped near 104 bytes.
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
	live := config.NewLive(cfg)
	st := store.New(live)

	held, _, err := st.CreatePad(store.CreateRequest{Project: "alpha", Author: "ping", Title: "held", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	free, _, err := st.CreatePad(store.CreateRequest{Project: "beta", Author: "ping", Title: "free", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}

	// The other session: a live relay of this store already watching `held` as ping.
	other := relay.NewServer(relay.SocketPath(cfg.RootDir), live, func(string) error { return nil })
	if err := other.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = other.Run(ctx) }()
	if err := relay.Register(other.Socket(), relay.Registration{Ref: held.Ref(), Author: "ping", Since: 1, Wake: relayWake}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	regs, err := planRejoin(&out, live, relay.SocketPath(cfg.RootDir), "ping", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].Ref != free.Ref() || regs[0].Since != 1 || regs[0].Author != "ping" {
		t.Fatalf("regs = %+v, want only %s from §1 as ping", regs, free.Ref())
	}
	text := out.String()
	first := strings.SplitN(text, "\n", 2)[0]
	if !strings.Contains(first, "rejoining 1 pad(s) as ping") || !strings.Contains(first, "1 already watched as ping by another relay") {
		t.Errorf("summary line does not count what was left out: %q", first)
	}
	if !strings.Contains(text, "warning: "+held.Ref()) {
		t.Errorf("no warning naming the duplicated pad:\n%s", text)
	}

	// A different name on the same pad is not a duplicate: two agents talking on one pad
	// is the ordinary case.
	out.Reset()
	regs, err = planRejoin(&out, live, relay.SocketPath(cfg.RootDir), "pong", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 || strings.Contains(out.String(), "already watched") {
		t.Errorf("pong takes part in nothing and duplicates nothing: %+v / %q", regs, out.String())
	}
}
