// Package docs bundles the authoritative OpenAPI contract and its read-only UI.
package docs

import (
	"embed"
	"net/http"
)

//go:embed openapi.json ui.html ui.css ui.js
var assets embed.FS

// Serve handles only fixed documentation resources. It never serves host files.
// The daemon's TLS listener supplies mTLS; these static resources need no HMAC.
func Serve(w http.ResponseWriter, r *http.Request) bool {
	var file, contentType string
	switch r.URL.Path {
	case "/docs":
		file = "ui.html"
		contentType = "text/html; charset=utf-8"
	case "/docs/style.css":
		file = "ui.css"
		contentType = "text/css; charset=utf-8"
	case "/docs/app.js":
		file = "ui.js"
		contentType = "text/javascript; charset=utf-8"
	case "/openapi.json":
		file = "openapi.json"
		contentType = "application/json"
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"REQUEST_INVALID","message":"The operation could not be completed.","request_id":""}}`))
		return true
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	b, err := assets.ReadFile(file)
	if err != nil {
		http.Error(w, "Documentation unavailable", 500)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(b)
	return true
}
