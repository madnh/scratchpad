package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/buildinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"

	"golang.org/x/sys/unix"
)

// newDoctorCmd inspects how the Scratchpad dir resolves and what is actually there,
// with ZERO side effects: it never bootstraps the default dir, never creates a file,
// and opens pads read-only. Unlike every other command it does not error out on an
// unresolved dir — the failed resolution IS the thing being diagnosed, so it reports
// the trail (flag, env, default marker pointer) instead.
func newDoctorCmd() *cobra.Command {
	var dir dirFlags
	var wantContent, wantVerdict, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose dir resolution and store state (no side effects)",
		Long: "Report which binary is running, how the Scratchpad dir resolves (flag, env, default\n" +
			"marker pointer, default), and the store's health. doctor never creates or writes\n" +
			"anything — a missing dir or file is reported, not fixed (and the default dir is NOT\n" +
			"auto-bootstrapped here).\n\n" +
			"Content (--content) and Verdict (--verdict) are opt-in; --json is machine-readable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := runDoctor(&dir, wantContent, wantVerdict)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			rep.writeText(cmd.OutOrStdout())
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.BoolVar(&wantContent, "content", false, "also list every pad (ref + section count) — the strongest 'is this the right store?' signal")
	f.BoolVar(&wantVerdict, "verdict", false, "also print a plain-language conclusion and next step")
	f.BoolVar(&asJSON, "json", false, "emit the report as JSON instead of text")
	return cmd
}

// doctorReport is the full diagnostic, serializable to JSON.
type doctorReport struct {
	Binary  string `json:"binary"`
	Version string `json:"version"`
	// OnPath reports whether the RUNNING binary's own directory is in $PATH. A
	// same-named different file is reported separately as PathShadow.
	OnPath bool `json:"on_path"`
	// PathShadow is a DIFFERENT same-named file the command name resolves to on $PATH
	// ("" when none). Determined purely at the OS level (LookPath + inode comparison);
	// the file is NEVER executed to learn more about it.
	PathShadow string `json:"path_shadow,omitempty"`
	Cwd        string `json:"cwd"`

	DirFlag    string `json:"dir_flag,omitempty"`
	DirEnv     string `json:"dir_env,omitempty"`
	DefaultDir string `json:"default_dir,omitempty"`
	// DefaultDirState explains the fixed default location: initialized / not
	// bootstrapped yet / marker missing but dir exists.
	DefaultDirState string `json:"default_dir_state,omitempty"`
	ConfigPointer   string `json:"config_pointer,omitempty"` // the default marker's `dir` field, when set

	Dir          string `json:"dir"` // resolved abs, "" if unresolved
	DirSource    string `json:"dir_source,omitempty"`
	ResolveError string `json:"resolve_error,omitempty"`
	MarkerFile   string `json:"marker_file,omitempty"`
	MarkerError  string `json:"marker_error,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	Instance     string `json:"instance,omitempty"`
	ProjectsDir  string `json:"projects_dir,omitempty"`
	Socket       string `json:"socket,omitempty"`
	SocketLive   bool   `json:"socket_live,omitempty"`

	Store   *doctorStore    `json:"store,omitempty"`
	Content []doctorPadInfo `json:"content,omitempty"`
	Verdict []string        `json:"verdict,omitempty"`
}

// doctorStore is the physical state of the projects/ store, gathered by stat/list
// only — nothing is opened for writing.
type doctorStore struct {
	Exists       bool   `json:"exists"`
	Writable     bool   `json:"writable"`
	ProjectCount int    `json:"project_count"`
	PadCount     int    `json:"pad_count"`
	LastPad      string `json:"last_pad,omitempty"` // most recently modified pad file
	LastPadParse string `json:"last_pad_parse,omitempty"`
	ListWarnings int    `json:"list_warnings,omitempty"`
}

// doctorPadInfo is one pad's --content entry.
type doctorPadInfo struct {
	Ref          string `json:"ref"`
	SectionCount int    `json:"section_count"`
	Protected    bool   `json:"protected"`
	Title        string `json:"title"`
}

func runDoctor(dir *dirFlags, wantContent, wantVerdict bool) *doctorReport {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "(unknown)"
	}
	rep := &doctorReport{
		Cwd:     cwd,
		Version: buildinfo.Get().String(),
		DirFlag: dir.dir,
		DirEnv:  os.Getenv(config.EnvDir),
	}
	// Identify WHICH binary is running (resolve symlinks), whether THIS file's dir is
	// on $PATH, and whether a same-named file elsewhere shadows it. LookPath only
	// searches and stats — it never executes anything.
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		rep.Binary = exe
		rep.OnPath = dirOnPath(filepath.Dir(exe))
		if lp, err := exec.LookPath(filepath.Base(exe)); err == nil {
			if real, err := filepath.EvalSymlinks(lp); err == nil {
				lp = real
			}
			if !sameFile(lp, exe) {
				rep.PathShadow = lp
			}
		}
	}

	if def, err := config.DefaultDir(); err == nil {
		rep.DefaultDir = def
		switch {
		case config.IsInitialized(def):
			rep.DefaultDirState = "initialized"
			if cfg, err := config.LoadDir(def); err == nil {
				rep.ConfigPointer = cfg.Dir
			}
		default:
			if _, err := os.Stat(def); err == nil {
				rep.DefaultDirState = "exists but has no marker"
			} else {
				rep.DefaultDirState = "not bootstrapped yet (created on first regular command)"
			}
		}
	}

	// Re-do the resolution WITHOUT the bootstrap side effect: explicit sources must be
	// initialized; the default counts only when it already is.
	resolved, source, rerr := probeResolve(dir.dir, rep)
	if rerr != nil {
		rep.ResolveError = rerr.Error()
		if wantVerdict {
			rep.Verdict = rep.verdict()
		}
		return rep
	}
	rep.Dir = resolved
	rep.DirSource = source
	rep.MarkerFile = config.MarkerPath(resolved)

	cfg, lerr := config.LoadDir(resolved)
	if lerr != nil {
		rep.MarkerError = lerr.Error()
		if wantVerdict {
			rep.Verdict = rep.verdict()
		}
		return rep
	}
	rep.DisplayName = cfg.DisplayName
	rep.Instance = cfg.Instance
	rep.ProjectsDir = cfg.ProjectsDir
	rep.Socket = cfg.SocketPath
	if fi, err := os.Stat(cfg.SocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
		rep.SocketLive = true
	}

	rep.Store = statStore(cfg)
	if wantContent {
		st := store.New(cfg.ProjectsDir, cfg.Limits)
		if pads, _, err := st.List(""); err == nil {
			for _, p := range pads {
				rep.Content = append(rep.Content, doctorPadInfo{
					Ref: p.Ref, SectionCount: p.SectionCount, Protected: p.Protected, Title: p.Title,
				})
			}
		}
	}
	if wantVerdict {
		rep.Verdict = rep.verdict()
	}
	return rep
}

// probeResolve mirrors config.Resolve's precedence but NEVER bootstraps: doctor must
// not create the thing it is diagnosing.
func probeResolve(flagDir string, rep *doctorReport) (string, string, error) {
	if s := strings.TrimSpace(flagDir); s != "" {
		abs, _ := filepath.Abs(s)
		if !config.IsInitialized(abs) {
			return "", "", fmt.Errorf("dir %q (flag --dir) is not initialized: no %s there", abs, config.MarkerFilename)
		}
		return abs, "flag --dir", nil
	}
	if s := strings.TrimSpace(os.Getenv(config.EnvDir)); s != "" {
		abs, _ := filepath.Abs(s)
		if !config.IsInitialized(abs) {
			return "", "", fmt.Errorf("dir %q (env %s) is not initialized: no %s there", abs, config.EnvDir, config.MarkerFilename)
		}
		return abs, "env " + config.EnvDir, nil
	}
	def := rep.DefaultDir
	if def == "" {
		return "", "", fmt.Errorf("cannot determine the home directory for the default dir")
	}
	if !config.IsInitialized(def) {
		return "", "", fmt.Errorf("default dir %s is not bootstrapped yet — any regular command will create it; doctor deliberately does not", def)
	}
	if p := strings.TrimSpace(rep.ConfigPointer); p != "" {
		abs, _ := filepath.Abs(p)
		if !config.IsInitialized(abs) {
			return "", "", fmt.Errorf("dir %q (config `dir` in %s) is not initialized: no %s there", abs, config.MarkerPath(def), config.MarkerFilename)
		}
		return abs, "config `dir` in " + config.MarkerPath(def), nil
	}
	return def, "default " + def, nil
}

// statStore inspects the projects/ store by stat/list only.
func statStore(cfg config.Config) *doctorStore {
	ds := &doctorStore{}
	fi, err := os.Stat(cfg.ProjectsDir)
	if err != nil || !fi.IsDir() {
		return ds
	}
	ds.Exists = true
	ds.Writable = unix.Access(cfg.ProjectsDir, unix.W_OK) == nil

	st := store.New(cfg.ProjectsDir, cfg.Limits)
	if projects, err := st.Projects(); err == nil {
		ds.ProjectCount = len(projects)
		for _, p := range projects {
			ds.PadCount += p.PadCount
		}
	}
	// Parse-check the most recently modified pad (read-only) — a quick "is the store
	// healthy?" probe without touching every file.
	if pads, warns, err := st.List(""); err == nil {
		ds.ListWarnings = len(warns)
		if len(pads) > 0 {
			ds.LastPad = pads[0].Ref
			ds.LastPadParse = "ok"
		}
		if len(warns) > 0 {
			ds.LastPadParse = warns[0]
		}
	}
	return ds
}

// verdict walks the failure modes outermost-in and states a conclusion + next step.
func (r *doctorReport) verdict() []string {
	name := filepath.Base(r.Binary)
	if name == "" || name == "." {
		name = "scratchpad"
	}
	switch {
	case r.ResolveError != "":
		return []string{"the Scratchpad dir does not resolve: " + r.ResolveError,
			"next: run any regular command to bootstrap the default dir, or `" + name + " init --dir <path>` for a custom one"}
	case r.MarkerError != "":
		return []string{"the dir resolves but its marker is invalid: " + r.MarkerError,
			"next: fix or remove " + r.MarkerFile}
	case r.Store == nil || !r.Store.Exists:
		return []string{"the marker is valid but the projects/ store is missing",
			"next: create " + r.ProjectsDir + " (mode 0700) or re-init the dir"}
	case !r.Store.Writable:
		return []string{"the store exists but is not writable by this user",
			"next: check ownership/permissions of " + r.ProjectsDir}
	case r.Store.ListWarnings > 0:
		return []string{fmt.Sprintf("the store works but %d pad file(s) fail to parse (see store group)", r.Store.ListWarnings),
			"next: inspect the reported pads; they are plain markdown"}
	default:
		return []string{fmt.Sprintf("everything checks out: %d project(s), %d pad(s)", r.Store.ProjectCount, r.Store.PadCount)}
	}
}

// writeText renders the report for humans, grouped the way the JSON is.
func (r *doctorReport) writeText(w io.Writer) {
	fmt.Fprintln(w, "▸ Resolution")
	fmt.Fprintf(w, "  binary        %s\n", r.Binary)
	fmt.Fprintf(w, "  version       %s\n", r.Version)
	fmt.Fprintf(w, "  on PATH       %t\n", r.OnPath)
	if r.PathShadow != "" {
		fmt.Fprintf(w, "  PATH shadow   %s  (⚠ command name resolves to a different file)\n", r.PathShadow)
	}
	fmt.Fprintf(w, "  cwd           %s\n", r.Cwd)
	if r.DirFlag != "" {
		fmt.Fprintf(w, "  --dir         %s\n", r.DirFlag)
	}
	if r.DirEnv != "" {
		fmt.Fprintf(w, "  %s  %s\n", config.EnvDir, r.DirEnv)
	}
	fmt.Fprintf(w, "  default dir   %s  (%s)\n", r.DefaultDir, r.DefaultDirState)
	if r.ConfigPointer != "" {
		fmt.Fprintf(w, "  config `dir`  %s\n", r.ConfigPointer)
	}
	if r.ResolveError != "" {
		fmt.Fprintf(w, "  dir           UNRESOLVED\n  error         %s\n", r.ResolveError)
		r.writeVerdict(w)
		return
	}
	fmt.Fprintf(w, "  dir           %s  (via %s)\n", r.Dir, r.DirSource)
	fmt.Fprintf(w, "  marker        %s\n", r.MarkerFile)
	if r.MarkerError != "" {
		fmt.Fprintf(w, "  marker error  %s\n", r.MarkerError)
		r.writeVerdict(w)
		return
	}
	fmt.Fprintf(w, "  display name  %s\n", r.DisplayName)
	fmt.Fprintf(w, "  instance      %s\n", r.Instance)
	fmt.Fprintf(w, "  projects dir  %s\n", r.ProjectsDir)
	fmt.Fprintf(w, "  socket        %s  (live: %t)\n", r.Socket, r.SocketLive)

	if r.Store != nil {
		fmt.Fprintln(w, "\n▸ Store")
		fmt.Fprintf(w, "  exists        %t\n", r.Store.Exists)
		fmt.Fprintf(w, "  writable      %t\n", r.Store.Writable)
		fmt.Fprintf(w, "  projects      %d\n", r.Store.ProjectCount)
		fmt.Fprintf(w, "  pads          %d\n", r.Store.PadCount)
		if r.Store.LastPad != "" {
			fmt.Fprintf(w, "  last pad      %s  (parse: %s)\n", r.Store.LastPad, r.Store.LastPadParse)
		}
		if r.Store.ListWarnings > 0 {
			fmt.Fprintf(w, "  warnings      %d pad file(s) fail to parse\n", r.Store.ListWarnings)
		}
	}
	if len(r.Content) > 0 {
		fmt.Fprintln(w, "\n▸ Content")
		for _, p := range r.Content {
			prot := ""
			if p.Protected {
				prot = "  [protected]"
			}
			fmt.Fprintf(w, "  %-24s %3d section(s)%s  %s\n", p.Ref, p.SectionCount, prot, p.Title)
		}
	}
	r.writeVerdict(w)
}

func (r *doctorReport) writeVerdict(w io.Writer) {
	if len(r.Verdict) == 0 {
		return
	}
	fmt.Fprintln(w, "\n▸ Verdict")
	for _, v := range r.Verdict {
		fmt.Fprintf(w, "  %s\n", v)
	}
}

// dirOnPath reports whether dir is one of the $PATH entries (compared by inode so
// symlinked PATH entries still match).
func dirOnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == "" {
			continue
		}
		if sameFile(p, dir) {
			return true
		}
	}
	return false
}

// sameFile compares two paths by inode/device (never by executing anything).
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
