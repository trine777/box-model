package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// R7 trust-tailnet: a request from a Tailscale tailnet source IP bypasses
// the Bearer check; public sources still require the token; with the flag
// off, everyone needs the token.

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// serve runs one request through withBearer with a chosen RemoteAddr and
// optional Authorization header, returning the status code.
func serve(trustTailnet bool, token, remoteAddr, authHeader string) int {
	h := withBearer(trustTailnet, token, okHandler())
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.RemoteAddr = remoteAddr
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestTrustTailnet_TailnetIPv4SkipsBearer(t *testing.T) {
	// 100.83.33.126 is in 100.64.0.0/10. No Authorization header at all.
	if code := serve(true, "secret", "100.83.33.126:54321", ""); code != http.StatusOK {
		t.Errorf("tailnet IPv4 should bypass Bearer, got %d", code)
	}
}

func TestTrustTailnet_TailnetIPv6SkipsBearer(t *testing.T) {
	// fd7a:115c:a1e0:: is the Tailscale ULA prefix.
	if code := serve(true, "secret", "[fd7a:115c:a1e0::1234]:54321", ""); code != http.StatusOK {
		t.Errorf("tailnet IPv6 should bypass Bearer, got %d", code)
	}
}

func TestTrustTailnet_PublicIPStillNeedsToken(t *testing.T) {
	// 8.8.8.8 is public; trust-tailnet on, but no token → 401.
	if code := serve(true, "secret", "8.8.8.8:443", ""); code != http.StatusUnauthorized {
		t.Errorf("public source without token should be 401 even with trust-tailnet, got %d", code)
	}
	// public + correct token → 200.
	if code := serve(true, "secret", "8.8.8.8:443", "Bearer secret"); code != http.StatusOK {
		t.Errorf("public source with correct token should be 200, got %d", code)
	}
	// public + wrong token → 401.
	if code := serve(true, "secret", "8.8.8.8:443", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("public source with wrong token should be 401, got %d", code)
	}
}

func TestTrustTailnet_DisabledIgnoresSourceIP(t *testing.T) {
	// trust-tailnet OFF: even a tailnet IP must present the token.
	if code := serve(false, "secret", "100.83.33.126:54321", ""); code != http.StatusUnauthorized {
		t.Errorf("with trust-tailnet off, tailnet IP without token should be 401, got %d", code)
	}
	if code := serve(false, "secret", "100.83.33.126:54321", "Bearer secret"); code != http.StatusOK {
		t.Errorf("with trust-tailnet off, tailnet IP with token should be 200, got %d", code)
	}
}

func TestTrustTailnet_NonTailnetPrivateRangesNotTrusted(t *testing.T) {
	// LAN / loopback are NOT tailnet — must still need token when trust on.
	for _, addr := range []string{"192.168.1.50:1234", "127.0.0.1:1234", "10.0.0.5:1234", "172.16.0.9:1234"} {
		if code := serve(true, "secret", addr, ""); code != http.StatusUnauthorized {
			t.Errorf("%s is not tailnet; should be 401 without token, got %d", addr, code)
		}
	}
}

func TestIsTailnetSource(t *testing.T) {
	cases := map[string]bool{
		"100.64.0.0:1":          true,
		"100.83.33.126:54321":   true,
		"100.127.255.255:9":     true,
		"100.63.255.255:1":      false, // just below CGNAT block
		"100.128.0.0:1":         false, // just above
		"8.8.8.8:443":           false,
		"192.168.1.1:80":        false,
		"[fd7a:115c:a1e0::1]:7": true,
		"[fd7b:115c:a1e0::1]:7": false, // wrong ULA prefix
		"garbage":               false,
	}
	for addr, want := range cases {
		if got := isTailnetSource(addr); got != want {
			t.Errorf("isTailnetSource(%q) = %v, want %v", addr, got, want)
		}
	}
}
