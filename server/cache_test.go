package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The hub updates in place and serves the dashboard from its own binary, so a
// browser holding an older copy of the code than the hub it is talking to
// produces the worst kind of bug: present on the server, absent in the page.
func TestStaticAssetsAreRevalidated(t *testing.T) {
	s := &Server{cfg: Config{}}
	mux := http.NewServeMux()
	s.routes(mux)
	for _, path := range []string{"/static/app.js", "/static/style.css", "/static/viewer.js"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, w.Code)
			continue
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", path, cc)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s: empty", path)
		}
	}
}
