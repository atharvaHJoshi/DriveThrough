package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServesEmbeddedIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

// A deep link must return the shell rather than a 404 so the client-side router
// can resolve it after a hard refresh.
func TestUnknownPathFallsBackToShell(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bookings/abc-123", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("expected the SPA shell body")
	}
}

// An unmatched API route must not be answered with HTML; a JSON client would
// otherwise parse the shell and report a bogus success.
func TestUnknownAPIPathReturnsJSON404(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestNonGetIsRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/anything", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestCacheHeaders(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/assets/index-abc123.js", "public, max-age=31536000, immutable"},
		{"/favicon.svg", "no-cache"},
		{"/index.html", "no-cache"},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		setCacheHeaders(rec, tc.path)
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("%s: cache-control = %q, want %q", tc.path, got, tc.want)
		}
	}
}
