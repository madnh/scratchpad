package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// dirFlags binds the shared --dir flag and resolves it to a loaded config + store,
// the way serve/doctor/pad all need. Resolution is directory-level (self-contained:
// marker + guide + projects/ + socket together); the DEFAULT dir (~/.scratchpad)
// auto-bootstraps on first use, while an explicit dir must already be initialized.
type dirFlags struct {
	dir string
}

func (d *dirFlags) bind(cmd *cobra.Command) {
	def, _ := config.DefaultDir()
	cmd.Flags().StringVar(&d.dir, "dir", "",
		fmt.Sprintf("Scratchpad dir (default %s, auto-bootstrapped; env %s)", def, config.EnvDir))
}

// resolve finds the Scratchpad dir (flag → env → default-marker pointer → default,
// bootstrapping only the default) and loads its marker.
func (d *dirFlags) resolve() (cfg config.Config, dir, source string, err error) {
	return config.Resolve(d.dir)
}

// open resolves the dir and builds the store over its projects/ root.
func (d *dirFlags) open() (*store.Store, config.Config, error) {
	cfg, _, _, err := d.resolve()
	if err != nil {
		return nil, config.Config{}, err
	}
	return store.New(cfg.ProjectsDir, cfg.Limits), cfg, nil
}
