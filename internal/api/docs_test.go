package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentationResourcesAreStatic(t *testing.T) {
	// No Engine/Auth is supplied: docs must not consult privileged state or APIs.
	a := &API{}
	for path, media := range map[string]string{"/docs": "text/html", "/openapi.json": "application/json", "/docs/style.css": "text/css", "/docs/app.js": "text/javascript"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), media) || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("documentation resource %s: %d", path, w.Code)
		}
		if path == "/openapi.json" && !json.Valid(w.Body.Bytes()) {
			t.Fatal("invalid embedded contract")
		}
	}
	for _, path := range []string{"/docs?url=https://evil.invalid/spec", "/openapi.json?token=anything", "/d%6fcs"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatalf("accepted noncanonical docs request: %s (%d)", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("POST", "/docs", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal("docs accepted a mutation")
	}
}
