package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/madnh/scratchpad/internal/pad"
)

// Error codes for a config WRITE. They live here rather than with the pad vocabulary
// because they are raised here; the CodedError machinery is shared so every surface keeps
// one way of branching on a failure.
const (
	// CodeConfigStale: the writer quoted a version of the marker that is no longer on
	// disk. Somebody else saved in between.
	CodeConfigStale = "config_stale"
	// CodeConfigReadOnly: the write touched a group this surface may not change.
	CodeConfigReadOnly = "config_readonly"
)

// DigestLen is how much of the marker's sha256 a write quotes — the same length the rules
// use, for the same reason: long enough to be unambiguous, short enough to read aloud.
const DigestLen = pad.DigestLen

// The marker's groups, by their field names in the file. A write names the ones it is
// allowed to change and UpdateMarker refuses anything else — so what a surface may edit is
// enforced at the FILE, not only by which fields that surface happens to assign.
const (
	GroupDisplayName    = "display_name"
	GroupDefaultProject = "default_project"
	GroupLimits         = "limits"
	GroupWait           = "wait"
	GroupRules          = "rules"
	GroupDir            = "dir"
	GroupInstance       = "instance"
	GroupTCP            = "tcp"
	GroupUI             = "ui"
	GroupSchema         = "schema header"
)

// OperatorEditable is what a person may change from a SURFACE (today: the Web UI's
// settings page).
//
// Note what is absent. `tcp` holds the bearer-token digests and `ui` holds no_auth, so
// either would let a browser session widen who can reach this deployment. `rules` decides
// whether an agent may rewrite the operator's standing instructions — the store's memory
// of being burned here is that a privilege must be something the calling code holds, never
// something a request can name, and "the handler happens not to assign that field" is one
// forgetful edit away from not being true. Naming the set makes the file enforce it too.
//
// `instance` and `dir` are absent for the duller reason that a running process has already
// bound a socket and resolved a store.
var OperatorEditable = []string{GroupDisplayName, GroupDefaultProject, GroupLimits, GroupWait}

// MarkerDigest fingerprints the marker file's bytes as they are on disk.
//
// It is over the RAW bytes rather than the parsed Config, so a change nobody modelled —
// a field from a future schema, a hand-added comment-shaped key — still counts as a
// change. A version check that only sees what it understands is not a version check.
func MarkerDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:DigestLen]
}

// ReadMarker returns the marker's parsed config together with the digest of the exact
// bytes it was parsed from. A surface offering an edit form reads both and hands the
// digest back on save.
//
// The Config here is RAW — no defaults applied, no derived paths — because it is what
// will be written back. Round-tripping a defaulted config would silently promote every
// built-in default into an explicit setting in the operator's file, and then a later
// change of default would not reach a deployment that never chose it.
func ReadMarker(dir string) (Config, string, error) {
	c, _, digest, err := readMarkerRaw(dir)
	return c, digest, err
}

// readMarkerRaw is ReadMarker plus the bytes it parsed. The bytes matter to a WRITE: they
// carry whatever the file says that this binary does not model, and a save must not throw
// that away (see writeMarkerAtomic).
func readMarkerRaw(dir string) (Config, []byte, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, nil, "", err
	}
	raw, err := os.ReadFile(MarkerPath(abs))
	if err != nil {
		return Config{}, nil, "", fmt.Errorf("read config %s: %w", MarkerPath(abs), err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, nil, "", fmt.Errorf("parse config %s: %w", MarkerPath(abs), err)
	}
	return c, raw, MarkerDigest(raw), nil
}

// UpdateMarker is the compare-and-set on the marker: read what is there, refuse unless
// the caller quoted that version, apply mutate, and replace the file atomically.
//
// It is a separate function from WriteMarker, which refuses to overwrite anything — that
// one belongs to `init`, whose whole job is to not clobber a live deployment. This one is
// the edit, and it exists because an edit needs the two things init does not: a version
// check, and a guarantee that a reader never sees the file half-written.
//
// allowed names the groups mutate may change (see OperatorEditable). A mutation that
// moves anything else is REFUSED, not written — the check is on the result rather than on
// the caller's good intentions, so "the handler only assigns four fields" stops being the
// single thing standing between a request and the TCP tokens or the rules policy.
//
// The check and the write are not one atomic step, deliberately — the same trade-off the
// rules levels make. What this defends against is measured in minutes (two tabs, or a tab
// left open while somebody edited the file), not microseconds.
func UpdateMarker(dir, ifDigest string, allowed []string, mutate func(*Config) error) (Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	cur, raw, digest, err := readMarkerRaw(abs)
	if err != nil {
		return Config{}, err
	}
	if ifDigest == "" {
		return Config{}, pad.Coded(CodeConfigStale,
			"this change did not say which version of the configuration it is replacing;"+
				" read the current one and send its digest (now %s)", digest)
	}
	if !strings.EqualFold(ifDigest, digest) {
		return Config{}, pad.Coded(CodeConfigStale,
			"the configuration changed since you read it (you quoted %s, it is now %s);"+
				" re-read it and apply your change on top", ifDigest, digest)
	}

	next := cur
	if err := mutate(&next); err != nil {
		return Config{}, err
	}
	if err := checkChangedWithin(cur, next, allowed); err != nil {
		return Config{}, err
	}
	// Validate through the same path a load takes, so a value the loader would reject can
	// never be written: a marker that cannot be loaded takes every command down with it,
	// including the one an operator would use to fix it.
	//
	// The bounds run against what was WRITTEN and the relationships against what it MEANS.
	// applyDefaults turns any non-positive number into the built-in, so checking bounds on
	// the defaulted copy would wave a negative straight through.
	effective := next
	effective.applyDefaults()
	if err := effective.validateRules(); err != nil {
		return Config{}, pad.Coded(pad.CodeInvalidInput, "%s", err.Error())
	}
	if err := checkLimits(next.Limits); err != nil {
		return Config{}, err
	}
	if err := checkWait(next.Wait, effective.Wait); err != nil {
		return Config{}, err
	}
	if err := checkDefaultProject(next.DefaultProject); err != nil {
		return Config{}, err
	}
	if err := checkDisplayName(next.DisplayName); err != nil {
		return Config{}, err
	}
	if err := writeMarkerAtomic(abs, next, raw, allowed); err != nil {
		return Config{}, err
	}
	return next, nil
}

// checkChangedWithin refuses a write that moved a group the caller was not allowed to
// move. It compares the RESULT against what was on disk, so it does not matter how the
// mutation reached the field — assignment, a struct copy, a future helper.
func checkChangedWithin(cur, next Config, allowed []string) error {
	var forbidden []string
	for _, g := range changedGroups(cur, next) {
		if !slices.Contains(allowed, g) {
			forbidden = append(forbidden, g)
		}
	}
	if len(forbidden) == 0 {
		return nil
	}
	return pad.Coded(CodeConfigReadOnly,
		"%s may not be changed here: a port and a socket are already bound, and who may write"+
			" the rules is not a browser session's to grant. Edit %s directly (and restart for"+
			" the ones that are bound)", strings.Join(forbidden, ", "), MarkerFilename)
}

// changedGroups names every group that differs between two markers — hot and cold alike.
// One list, so "what may this surface write" and "what needs a restart" are asked of the
// same function instead of drifting apart in two places.
func changedGroups(cur, next Config) []string {
	var out []string
	if cur.Type != next.Type || cur.Version != next.Version {
		out = append(out, GroupSchema)
	}
	if cur.DisplayName != next.DisplayName {
		out = append(out, GroupDisplayName)
	}
	if cur.DefaultProject != next.DefaultProject {
		out = append(out, GroupDefaultProject)
	}
	if !SameLimits(cur.Limits, next.Limits) {
		out = append(out, GroupLimits)
	}
	if cur.Wait != next.Wait {
		out = append(out, GroupWait)
	}
	if cur.Rules != next.Rules {
		out = append(out, GroupRules)
	}
	return append(out, ColdChanges(cur, next)...)
}

// checkLimits keeps a written limit inside what the format can honestly carry. A zero is
// allowed and means "use the default" — that is how the marker has always spelled it.
func checkLimits(l Limits) error {
	for _, f := range []struct {
		name string
		v    int
		max  int
	}{
		{"max_title_kb", l.MaxTitleKB, 1024},
		{"max_content_kb", l.MaxContentKB, 1024 * 16},
		{"max_sections_per_pad", l.MaxSectionsPerPad, 1_000_000},
		{"max_pads_per_project", l.MaxPadsPerProject, 1_000_000},
	} {
		if f.v < 0 {
			return pad.Coded(pad.CodeInvalidInput, "limits.%s cannot be negative", f.name)
		}
		if f.v > f.max {
			// The ceiling is not arbitrary: maxPadBytes is derived from these, so a
			// wild value here is a promise to read a wild amount of memory later.
			return pad.Coded(pad.CodeInvalidInput, "limits.%s is at most %d (got %d)", f.name, f.max, f.v)
		}
	}
	// A percentage outside 1..100 is refused rather than clamped: the loader would drop it
	// silently, and an operator who typed 800 meaning 80 deserves to be told, not to
	// discover months later that the warning never fired.
	for _, p := range l.WarnAtPercent {
		if p == 0 && len(l.WarnAtPercent) == 1 {
			continue // the explicit "off" spelling
		}
		if p < 1 || p > 100 {
			return pad.Coded(pad.CodeInvalidInput,
				"limits.warn_at_percent takes percentages of 1..100 (got %d); use [0] to turn the warnings off", p)
		}
	}
	return nil
}

// checkWait keeps the long-poll bounds coherent: a default longer than the cap would be
// clamped on every call, which is a setting that silently does not mean what it says.
//
// written is what goes in the file (where 0 means "default"); effective is what it
// resolves to. The relationship can only be judged on the second — leaving max_s unset
// beside an explicit default_s is a perfectly good marker.
func checkWait(written, effective Wait) error {
	if written.DefaultS < 0 || written.MaxS < 0 {
		return pad.Coded(pad.CodeInvalidInput, "wait values cannot be negative")
	}
	if written.MaxS > 3600 {
		return pad.Coded(pad.CodeInvalidInput, "wait.max_s is at most 3600 (got %d)", written.MaxS)
	}
	if effective.DefaultS > effective.MaxS {
		return pad.Coded(pad.CodeInvalidInput,
			"wait.default_s (%d) cannot exceed wait.max_s (%d)", effective.DefaultS, effective.MaxS)
	}
	return nil
}

// projectNameRe is the store's project naming rule, restated here because this is where a
// project name is WRITTEN. Without it, the marker would happily accept "My Project" and
// then every pad create against the deployment would fail with invalid_project_name — a
// setting that saved cleanly and broke something else. The store still validates on the
// way in; nothing builds a path from this value unchecked.
var projectNameRe = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// checkDefaultProject rejects a default project the store could never use. Blank is fine:
// it means "the built-in default".
func checkDefaultProject(name string) error {
	if name == "" || projectNameRe.MatchString(name) {
		return nil
	}
	return pad.Coded(pad.CodeInvalidProjectName,
		"default_project %q is invalid: only a-z and 0-9 are allowed (no '-' or '_'), max 64 chars", name)
}

// maxDisplayNameBytes keeps the deployment name to something a header can show. It is not
// a security bound — the request body is already capped — it just stops a paste of an
// entire document from becoming the name of the deployment.
const maxDisplayNameBytes = 200

func checkDisplayName(name string) error {
	if len(name) > maxDisplayNameBytes {
		return pad.Coded(pad.CodeInvalidInput,
			"display_name is %d bytes; the limit is %d", len(name), maxDisplayNameBytes)
	}
	if strings.ContainsAny(name, "\n\r") {
		return pad.Coded(pad.CodeInvalidInput, "display_name must be a single line")
	}
	return nil
}

// writeMarkerAtomic replaces the marker with a temp file + rename, so a concurrent reader
// gets either the old config or the new one and never a truncated file. The derived paths
// are cleared first: they are computed from the dir at load time and persisting them would
// turn a self-contained directory into one that remembers where it used to live.
//
// original is the file as it was read. The new marker is MERGED onto it rather than
// marshalled over it, because a round trip through Config keeps only what Config models —
// so a key this binary has never heard of (a note the operator keeps in the file, a
// setting a newer build added) would vanish on somebody pressing Save. A save is an edit
// to the groups it names, not a rewrite of the file.
func writeMarkerAtomic(dir string, c Config, original []byte, allowed []string) error {
	c.RootDir = ""
	c.ProjectsDir = ""
	c.SocketPath = ""
	b, err := mergeMarker(original, c, allowed)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, MarkerFilename+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), MarkerPath(dir))
}

// modelledKeys is the set of top-level marker keys this binary's Config understands, read
// off the struct tags rather than listed by hand — a list maintained separately from the
// struct is a list that goes stale the first time somebody adds a field.
var modelledKeys = func() map[string]bool {
	m := map[string]bool{}
	t := reflect.TypeFor[Config]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			m[name] = true
		}
	}
	return m
}()

// derivedKeys are computed from the dir at load time and never belong in the file.
var derivedKeys = map[string]bool{"root_dir": true, "projects_dir": true, "socket_path": true}

// mergeMarker renders the marker to write: the file it started from, with the groups this
// write was ALLOWED to touch replaced by what c now says.
//
// The rules it follows, in order:
//   - a derived key is dropped;
//   - a key outside `allowed` is copied through byte for byte — whether or not Config
//     models it. That covers a note the operator keeps in the file, a setting a newer
//     build added, AND a modelled group this write had no business rewriting. An explicit
//     `"ui": {"no_auth": false}` is an assertion somebody made on purpose; a save about
//     limits must not quietly delete it just because absent happens to mean the same;
//   - a key inside `allowed` is taken from c, and REMOVED when c no longer emits it (the
//     value went back to its zero, which the marker spells as "absent") — so blanking a
//     field in a form is the same act as deleting the line.
//
// Key order follows the original file, with anything new appended: an edit should produce
// a small diff, not reshuffle a file somebody maintains by hand.
func mergeMarker(original []byte, c Config, allowed []string) ([]byte, error) {
	encoded, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &next); err != nil {
		return nil, err
	}

	var current map[string]json.RawMessage
	var order []string
	if len(original) > 0 {
		if err := json.Unmarshal(original, &current); err != nil {
			return nil, err
		}
		if order, err = objectKeyOrder(original); err != nil {
			return nil, err
		}
	}

	out := make([]byte, 0, len(original)+len(encoded))
	out = append(out, '{')
	written := make(map[string]bool, len(next))
	emit := func(key string, value json.RawMessage) {
		if len(written) > 0 {
			out = append(out, ',')
		}
		written[key] = true
		k, _ := json.Marshal(key)
		out = append(out, k...)
		out = append(out, ':')
		out = append(out, value...)
	}
	for _, key := range order {
		switch {
		case derivedKeys[key]:
			// Never persisted: LoadDir computes these from the dir.
		case !slices.Contains(allowed, key):
			// Not this write's to touch — including a group Config models. Copied byte
			// for byte so the file keeps saying exactly what its author wrote.
			emit(key, current[key])
		case next[key] != nil:
			emit(key, next[key])
		default:
			// Allowed, and c no longer emits it: the field was cleared back to its
			// default, and "unset" is spelled by absence.
		}
	}
	// Anything c emits that the file did not have yet, appended in a stable order.
	for _, key := range slices.Sorted(maps.Keys(next)) {
		if !written[key] && !derivedKeys[key] && slices.Contains(allowed, key) {
			emit(key, next[key])
		}
	}
	out = append(out, '}')

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		return nil, err
	}
	return append(pretty.Bytes(), '\n'), nil
}

// objectKeyOrder lists a JSON object's keys in the order the file writes them, which
// encoding/json throws away when it decodes into a map.
func objectKeyOrder(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("config is not a JSON object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("config has a non-string key")
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// IsConfigWriteError reports whether err came from this file's checks rather than from
// the filesystem, so a surface can tell "you sent something wrong" from "the disk said no".
func IsConfigWriteError(err error) bool {
	var ce *pad.CodedError
	return errors.As(err, &ce) && (ce.Code == CodeConfigStale || ce.Code == CodeConfigReadOnly)
}
