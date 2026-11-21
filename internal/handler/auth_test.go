package handler

import (
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/MiraiNoShiori/netdisk/internal/config"
)

func TestRegistrationClosed(t *testing.T) {
	s := Server{deps: Deps{Cfg: &config.Config{AllowRegister: false}, Logger: slog.Default()}}
	w := httptest.NewRecorder()
	s.authRegister(w, httptest.NewRequest("POST", "/api/v1/auth/register", nil))
	if w.Code != 403 {
		t.Fatalf("register = %d", w.Code)
	}
}

func TestCookieSecure(t *testing.T) {
	for _, tc := range []struct {
		mode, url, peer, proto string
		secure                 bool
	}{
		{"true", "http://disk", "192.0.2.2:80", "", true},
		{"false", "https://disk", "192.0.2.2:80", "", false},
		{"auto", "https://disk", "192.0.2.2:80", "", true},
		{"auto", "http://disk", "192.0.2.2:80", "https", false},
		{"auto", "http://disk", "127.0.0.1:80", "https", true},
		{"auto", "http://disk", "127.0.0.1:80", "http", false},
	} {
		s := Server{deps: Deps{Cfg: &config.Config{CookieSecure: tc.mode, TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}}}
		r := httptest.NewRequest("GET", tc.url, nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		if got := s.secureCookie(r); got != tc.secure {
			t.Errorf("%+v: secure=%v", tc, got)
		}
	}
}
