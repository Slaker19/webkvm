package api

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed novnc.mjs xterm.mjs xterm.css xterm-addon-fit.mjs
var staticFS embed.FS

func staticRouter() http.Handler {
	r := chi.NewRouter()

	r.Get("/novnc.mjs", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("novnc.mjs")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Write(data)
	})

	r.Get("/xterm.mjs", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("xterm.mjs")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Write(data)
	})

	r.Get("/xterm.css", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("xterm.css")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Write(data)
	})

	r.Get("/xterm-addon-fit.mjs", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("xterm-addon-fit.mjs")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Write(data)
	})

	return r
}
