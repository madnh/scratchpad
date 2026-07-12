// Package appinfo exposes the identity of the running binary, derived at runtime, so no
// user-facing string has to hardcode the command name. Help text and command-example
// hints all read Name() — rename or reinstall the binary under a different name and they
// follow it. Fixed FORMAT/spec identifiers (the config marker filename, the config `type`
// value, module import paths) are deliberately NOT derived from here: those name the data
// format, which does not change when the binary is renamed.
package appinfo

import (
	"os"
	"path/filepath"
)

// fallbackName is used only when the running executable's path can't be determined
// (os.Executable error). It is the project's canonical command name.
const fallbackName = "scratchpad"

// Name returns the base name of the running executable (how it was invoked), falling back
// to the canonical name when the path is unavailable or degenerate.
func Name() string {
	exe, err := os.Executable()
	if err != nil {
		return fallbackName
	}
	switch base := filepath.Base(exe); base {
	case "", ".", string(filepath.Separator):
		return fallbackName
	default:
		return base
	}
}
