package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowedOriginAndPreflight(t *testing.T) {
	mw := CORS([]string{"https://app.example.com"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 命中白名单的预检请求返回 204 并携带允许头。
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/files/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow-credentials = %q", got)
	}

	// 未在白名单内的来源不下发 Access-Control-Allow-Origin。
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/files/", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if got := w2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected allow-origin = %q", got)
	}
}

func TestShareVerifyRateLimit(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := AuthLimit(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/share/abc123/verify", nil)
		req.RemoteAddr = "192.0.2.10:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("req %d got %d", i, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/share/abc123/verify", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("31st share verify got %d, want 429", w.Code)
	}
}
