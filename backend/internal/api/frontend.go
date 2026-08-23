package api

import (
	"io/fs"
	"net/http"
	"strings"

	"webkvm/internal/frontend"
)

// frontendHandler serves the embedded Svelte SPA. Asset files (with
// extensions) are served directly; anything else falls back to
// index.html so client-side routing works after a page refresh.
func frontendHandler() http.Handler {
	distFS, err := fs.Sub(frontend.FS, "dist")
	if err != nil {
		// dist/ is empty (e.g. running from source without a frontend
		// build). Serve a friendly stub so the API endpoints stay
		// usable and the user knows what to do.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(frontendStub))
		})
	}
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" {
			serveIndex(distFS, w, r)
			return
		}
		if f, err := distFS.Open(upath); err == nil {
			info, _ := f.Stat()
			f.Close()
			if info != nil && !info.IsDir() {
				if strings.HasPrefix(upath, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: any unknown path returns index.html.
		serveIndex(distFS, w, r)
	})
}

func serveIndex(distFS fs.FS, w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		http.Error(w, "frontend not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

const frontendStub = `<!doctype html>
<html><head><meta charset="utf-8"><title>WebKVM</title></head>
<body style="font-family:system-ui;max-width:600px;margin:80px auto;padding:20px;line-height:1.6;color:#e4e4e7;background:#0f0f12">
<h1 style="color:#fff">WebKVM backend is running</h1>
<p>The frontend hasn't been built yet. On the build host run:</p>
<pre style="background:#1c1c22;padding:12px;border-radius:6px;color:#a5f3fc">cd frontend &amp;&amp; npm install &amp;&amp; npm run build
cd .. &amp;&amp; make build
sudo make install</pre>
<p>Or check the <a style="color:#7dd3fc" href="/api/system/status">/api/system/status</a> endpoint.</p>
</body></html>`
