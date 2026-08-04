package main

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
	"github.com/madnh/scratchpad/internal/watch"
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
//
// It hands back a *config.Live even though a CLI command exits before the marker could
// possibly change: every surface takes the same type, so nothing has to remember which
// ones are long-lived. The long-lived ones additionally call watchConfig.
func (d *dirFlags) open() (*store.Store, *config.Live, error) {
	cfg, _, _, err := d.resolve()
	if err != nil {
		return nil, nil, err
	}
	live := config.NewLive(cfg)
	return store.New(live), live, nil
}

// watchConfig keeps live in step with the marker on disk for as long as ctx runs, so an
// operator editing the config is obeyed by the process already running rather than only
// by the next one to start. `serve` calls it; the Web UI runs the same watcher inside
// its own server, alongside the one it already keeps on the pads.
func watchConfig(ctx context.Context, live *config.Live, dir string) {
	m := watch.ReloadConfig(dir, live)
	go func() {
		if err := m.Run(ctx); err != nil {
			log.Printf("config: watcher stopped: %v", err)
		}
	}()
}
