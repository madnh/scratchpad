package relay

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SocketDir is where a dir's relay sockets live. It is derived from the dir like
// projects/ and the MCP socket, never configured.
func SocketDir(rootDir string) string { return filepath.Join(rootDir, "relays") }

// LogPath is where `exec --log` writes when it is not given a path.
//
// One file for the whole dir, appended to, rather than one per run: a log that changes
// name every time cannot be followed with `tail -f`, and following it is the entire
// reason somebody turns it on. Several relays therefore share it, which is why every
// line carries its pid.
func LogPath(rootDir string) string { return filepath.Join(rootDir, "relay.log") }

// tempPrefix names the sockets that had to leave the dir because its path was too long
// for `sun_path`. They land in the system temp dir, which every store on the machine
// shares — so anything found there is matched on the Dir the relay reports, never on
// where the file happens to sit.
const tempPrefix = "scratchpad-relay-"

// CandidateSockets lists every path that might be a relay for this dir: the dir's own
// relays/, plus the temp-dir fallback.
//
// Looking in only one of the two is the bug this function exists to prevent — `relay`
// found nothing while a relay was running perfectly well, because the store's path was
// long enough to push its socket elsewhere and the lookup did not know that could happen.
func CandidateSockets(rootDir string) []string {
	var out []string
	for _, d := range []string{SocketDir(rootDir), os.TempDir()} {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
				continue
			}
			if d == os.TempDir() && !strings.HasPrefix(e.Name(), tempPrefix) {
				continue
			}
			out = append(out, filepath.Join(d, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// StaleSockets lists relay sockets under rootDir that nothing is listening on.
//
// A relay removes its own socket when it stops, so one is left behind only when the
// process was ended without a chance to clean up (`kill -9`, a power cut). The check is a
// dial rather than a look at the pid in the filename: pids are reused, and what actually
// matters is whether anything would answer.
//
// Reporting them is `doctor`'s job, and removing them is not — the dial says nobody is
// listening RIGHT NOW, which is a good reason to tell a person and a poor one to delete
// a file on their behalf.
func StaleSockets(rootDir string) []string {
	var stale []string
	for _, path := range CandidateSockets(rootDir) {
		conn, err := net.DialTimeout("unix", path, dialTimeout)
		if err != nil {
			// Only the dir's OWN sockets are reported. A dead file in the shared temp dir
			// cannot be attributed to a store — nothing answers to say which one it served
			// — and blaming this store for another's leftovers is worse than silence.
			if strings.HasPrefix(path, SocketDir(rootDir)) {
				stale = append(stale, path)
			}
			continue
		}
		conn.Close()
	}
	return stale
}
