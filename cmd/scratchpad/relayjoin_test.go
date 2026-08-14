package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/relay"
)

// The automatic registration has to cover every role an agent can hold on one pad:
// conversation participant, task owner, and task opener. None of these selectors wakes
// it for unrelated task traffic, unlike `any` or `tasks`.
func TestRelayWakeFollowsOnlyRelevantTraffic(t *testing.T) {
	wake, err := pad.ParseWake(relayWake)
	if err != nil {
		t.Fatal(err)
	}
	if !wake.Me || !wake.Mine || !wake.Opened {
		t.Fatalf("automatic relay selectors = %+v", wake)
	}
	if wake.Any || wake.Tasks || len(wake.TaskNos) != 0 {
		t.Fatalf("automatic relay must not subscribe to unrelated traffic: %+v", wake)
	}
}

// Replacing the CLI binary does not replace a relay process that is already running.
// An old relay rejects the new `opened` selector, so noteJoin retries with the subset the
// old process knows and tells the agent exactly what a restart unlocks.
func TestNoteJoinFallsBackForAnOlderRelay(t *testing.T) {
	dir, err := os.MkdirTemp("", "rj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "relay.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	wakes := make(chan []string, 2)
	errs := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				errs <- err
				return
			}
			var req struct {
				Wake []string `json:"wake"`
			}
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				_ = conn.Close()
				errs <- err
				return
			}
			wakes <- req.Wake
			res := map[string]any{"ok": i == 1}
			if i == 0 {
				res["error"] = `unknown wake selector "opened" (want any, me, mine, tasks or task:<n>)`
			}
			if err := json.NewEncoder(conn).Encode(res); err != nil {
				_ = conn.Close()
				errs <- err
				return
			}
			_ = conn.Close()
		}
		errs <- nil
	}()

	t.Setenv(relay.EnvSocket, sock)
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	noteJoin(cmd, "default-abc123", "pm", "", 3)

	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if got := <-wakes; !reflect.DeepEqual(got, relayWake) {
		t.Fatalf("first registration wake = %v", got)
	}
	if got := <-wakes; !reflect.DeepEqual(got, relayLegacyWake) {
		t.Fatalf("fallback registration wake = %v", got)
	}
	if !strings.Contains(stdout.String(), "relay: watching default-abc123 from §3") {
		t.Fatalf("successful fallback was not reported: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "restart scratchpad exec") {
		t.Fatalf("fallback did not explain its limit: %q", stderr.String())
	}
}
