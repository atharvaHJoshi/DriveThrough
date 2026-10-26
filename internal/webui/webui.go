// Package webui serves the compiled single-page application from the Go binary.
//
// The React build is embedded at compile time so a deployment is one artifact:
// the API, the assets, and the client-side routes all answer on the same origin.
// That removes the CORS, cookie, and WebSocket-upgrade problems that come with a
// separately hosted frontend, and it is why the Vite dev proxy mirrors this
// layout in development.
//
// The embedded directory is produced by `npm run build` in web/ (see
// web/vite.config.ts, which writes to internal/webui/dist). A placeholder
// index.html is committed so `go build` always finds at least one file, even on
// a fresh clone that has not built the UI yet.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// immutablePrefixes are the built asset paths whose filenames include a content
// hash (Vite emits assets/<name>-<hash>.<ext>). A hashed name can never refer to
// different bytes, so these may be cached indefinitely.
var immutablePrefixes = []string{"/assets/"}

// Handler returns the SPA server. It is intended to be mounted as the router's
// NotFound handler so that anything the API does not claim falls through to the
// client-side router.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Only reachable if the embed directive itself is wrong, which is a
		// build-time bug, not a runtime condition.
		panic("webui: embedded dist directory missing: " + err.Error())
	}

	files := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("webui: embedded index.html missing: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The API namespace is served by routes registered ahead of this
		// handler. Reaching here means the path does not exist, and answering
		// with index.html would hand a JSON caller an HTML 200.
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSONNotFound(w)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && fileExists(sub, name) {
			setCacheHeaders(w, r.URL.Path)
			files.ServeHTTP(w, r)
			return
		}

		// Unknown path: hand back the shell so the client-side router can decide
		// what to render. This is what makes deep links like /bookings/<id> work
		// on a hard refresh.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

func fileExists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func setCacheHeaders(w http.ResponseWriter, requestPath string) {
	for _, prefix := range immutablePrefixes {
		if strings.HasPrefix(requestPath, prefix) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			return
		}
	}
	// Everything else (favicon, root-level files) may change without a name
	// change, so it is revalidated rather than cached for a fixed window.
	w.Header().Set("Cache-Control", "no-cache")
}

func writeJSONNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"success":false,"error":{"code":"NOT_FOUND","message":"resource not found"}}`))
}
