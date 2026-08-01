package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// assetsFS holds the whole front end — the app shell, its page modules, and the
// vendored puredashboard library — compiled into the binary. `all:` is needed for the
// vendor tree; without it go:embed would skip nothing here today, but the prefix
// keeps a future underscore-named asset from silently disappearing.
//
//go:embed all:assets
var assetsFS embed.FS

// assetHandler serves the embedded front end. Unknown paths fall back to the shell:
// the router runs in hash mode, so a deep link is always `/#/...` and never actually
// asks the server for a page path — but a stray URL should land on the app rather
// than a bare 404, and /api/* is routed before this handler ever sees it.
func (s *Server) assetHandler() http.Handler {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("webui: embedded assets missing: " + err.Error())
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, statErr := fs.Stat(sub, clean); statErr != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		// The UI is served from a binary the person just started: never let a
		// browser hold a stale module after an upgrade.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
