// Package relay delivers a NUDGE into a running agent's terminal when a pad it takes
// part in has moved.
//
// It exists because waiting is the one thing an agent reliably fails at. `pad wait`
// prints one section and exits, so staying reachable means re-arming it after every
// turn — and an agent that is mid-task, or that simply forgot, is an agent the other
// side is talking to in vain. The relay inverts that: nobody has to remember to listen,
// because the listener is not the agent.
//
// Three properties keep it honest:
//
//   - **What arrives is a POINTER, never content.** The nudge names a pad and a section
//     number and nothing else. Content written by another agent, pasted into a prompt as
//     if the operator had typed it, is command injection with extra steps — and it is
//     injection whether or not the receiving process is healthy.
//
//   - **Registration is an ACT, not a claim.** A registration arrives over this process's
//     own control socket, from a child it spawned; `internal/relay` never infers "which
//     pads are mine" from an author string. Two sessions can call themselves `backend`
//     without either one hearing the other's traffic, because the name was never the
//     thing being trusted. Same law as PostRequest.SystemPost.
//
//   - **Nudges COALESCE, they do not queue.** Every nudge for a pad says the same thing —
//     "this pad moved, read from N" — so a later one subsumes an earlier one. Five new
//     sections are one line; three pads are one line. What is never sent twice is a
//     nudge for a pad whose previous nudge the agent has not acted on yet: repeating
//     "you have mail" to someone who has not read the first one buys nothing and costs a
//     turn.
//
// The registry lives in memory and dies with the process. That is deliberate: a
// registration is only meaningful while the terminal it points at is alive, and a file
// on disk would outlive both the terminal and the machine's last reboot.
package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// EnvSocket names the control socket of the relay a process should register with. It is
// set by `scratchpad exec` on the environment of the agent it spawns, which is what makes
// registration automatic: every scratchpad command the agent runs inherits it, so posting
// to a pad IS joining it, and joining is the only thing an agent ever has to do.
//
// A command that does not see this variable does nothing differently. The relay is opt-in
// at the point where the agent is launched, never a default.
const EnvSocket = "SCRATCHPAD_RELAY"

// dialTimeout bounds a registration attempt. Registration is best-effort decoration on
// top of a post that has already succeeded, so it must never be able to hang the command
// that is doing the real work.
const dialTimeout = 2 * time.Second

// Registration is one agent asking to be nudged about one pad.
//
// Since is the last section the agent is KNOWN to have seen — normally the section it
// just wrote. The relay never advances it on its own: a nudge is not evidence that
// anybody read anything, and a pointer that walks forward past unread sections would let
// the agent skip them by following only the newest nudge.
type Registration struct {
	Ref      string   `json:"ref"`
	Author   string   `json:"author"`
	Password string   `json:"password,omitempty"`
	Since    int      `json:"since,omitempty"`
	Wake     []string `json:"wake,omitempty"`
}

// request is the wire format of the control socket: one JSON object per connection.
type request struct {
	Op string `json:"op"`
	Registration
}

// response is what the server writes back before closing.
type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Status *Status `json:"status,omitempty"`
}

// Status is what one running relay knows about itself. It exists because a relay that
// works and a relay that never heard of your pad look identical from the outside — both
// are silence — and the difference is the whole question when nothing arrives.
type Status struct {
	PID     int    `json:"pid"`
	Socket  string `json:"socket"`
	Command string `json:"command,omitempty"`
	// Dir is the Scratchpad dir this relay serves. It is published because the socket is
	// not always inside that dir — a dir deep enough pushes it to the system temp dir,
	// which every store on the machine shares — so this is what says which relays are
	// THIS store's.
	Dir string `json:"dir,omitempty"`

	Watching []Watched `json:"watching"`

	// Nudges counts what has been delivered, LastNudge is the most recent line and
	// LastNudgeTS when it went out — so "nothing arrived" can be told apart from "it was
	// sent and the agent did not act on it".
	Nudges    int    `json:"nudges"`
	LastNudge string `json:"last_nudge,omitempty"`
	LastTS    int64  `json:"last_nudge_ts,omitempty"`
}

// Watched is one registration as an outsider sees it.
type Watched struct {
	Ref    string `json:"ref"`
	Author string `json:"author"`
	// Since is the section this agent is known to have read — the mark a nudge points at.
	Since int `json:"since"`
	// Pending means something new is waiting to be nudged about.
	Pending bool `json:"pending"`
	// NudgedTS is when this pad was last named in a nudge, 0 if never. While it is set,
	// the pad is inside its re-nudge floor: new traffic on it stays quiet.
	NudgedTS int64 `json:"nudged_ts,omitempty"`
}

// Query asks a relay what it is watching.
func Query(sock string) (*Status, error) {
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	if err := json.NewEncoder(conn).Encode(request{Op: "status"}); err != nil {
		return nil, err
	}
	var res response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return nil, err
	}
	if !res.OK || res.Status == nil {
		return nil, fmt.Errorf("relay did not answer: %s", res.Error)
	}
	return res.Status, nil
}

// Register tells the relay at sock that this agent is taking part in a pad.
//
// It returns an error only so callers can log one; every caller in this codebase ignores
// it. A relay that is gone, wedged, or was never there must not turn a successful post
// into a failed command — the pad file is the source of truth, and it has already been
// written by the time this runs.
func Register(sock string, reg Registration) error {
	if sock == "" {
		return errors.New("no relay socket")
	}
	if reg.Ref == "" || reg.Author == "" {
		return errors.New("registration needs a ref and an author")
	}
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	if err := json.NewEncoder(conn).Encode(request{Op: "register", Registration: reg}); err != nil {
		return err
	}
	var res response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("relay refused the registration: %s", res.Error)
	}
	return nil
}
