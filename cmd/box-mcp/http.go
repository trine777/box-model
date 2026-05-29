package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/obs"
)

// runHTTP serves the MCP server over Streamable-HTTP at cfg.httpAddr. It
// refuses to start without $BOX_API_TOKEN — remote MCP without auth would
// leak the entire KM repo to the public internet.
//
// The single server instance is shared across all incoming requests; the SDK's
// StreamableHTTPHandler manages per-connection sessions on its own.
//
// R0.19: in addition to /mcp + /blob/*, mount /items/<id>/blob — a one-shot
// download path for external machines that hold only an item id. Resolves
// the item, parses storage_uri, streams the blob.
func runHTTP(ctx context.Context, cfg config, srv *mcp.Server, svc *box.Service, observer obs.Observer, defaultCaller string, stderr io.Writer) error {
	token := os.Getenv("BOX_API_TOKEN")
	if token == "" {
		return fmt.Errorf("BOX_API_TOKEN env required for --http mode (refusing to serve unauthenticated)")
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	authedMCP := withBearer(cfg.trustTailnet, token, mcpHandler)

	// R0.18: local-FS blob layer mounted alongside the MCP routes. Routes
	// register on a sub-mux so we can put the whole thing behind the same
	// Bearer middleware without per-route boilerplate.
	root, err := blobRoot(cfg.boxHome)
	if err != nil {
		return fmt.Errorf("resolve blob root: %w", err)
	}
	blobMux := http.NewServeMux()
	if err := registerBlobRoutes(blobMux, root); err != nil {
		return fmt.Errorf("register blob routes: %w", err)
	}
	// R0.23 (F4): observability middleware wraps the blob + items routes so
	// box_observability can show /blob/upload / /items/<id>/blob traffic too,
	// not just MCP-tool calls. Bearer is outermost so 401s don't pollute the
	// metric stream.
	authedBlob := withBearer(cfg.trustTailnet, token, withObs(observer, "http.blob", blobMux))

	// R0.19: /items/<id>/blob is the one-shot download path for external
	// callers that hold only an item id. Same Bearer middleware.
	itemMux := http.NewServeMux()
	registerItemBlobRoute(itemMux, svc, root, defaultCaller)
	authedItem := withBearer(cfg.trustTailnet, token, withObs(observer, "http.items_blob", itemMux))

	mux := http.NewServeMux()
	mux.Handle("/mcp", authedMCP)
	mux.Handle("/mcp/", authedMCP) // trailing-slash variant some clients send
	mux.Handle("/blob/", authedBlob)
	mux.Handle("/items/", authedItem)
	// R14: human-observable dashboard (HTML). Same trust-tailnet Bearer
	// gate so a tailnet browser opens it token-free.
	dashMux := http.NewServeMux()
	registerDashboard(dashMux, svc, defaultCaller)
	mux.Handle("/dashboard", withDashboardAuth(cfg.trustTailnet, token, dashMux))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stderr, "box-mcp listening on %s (Streamable-HTTP /mcp + /blob/* + /items/<id>/blob ; Bearer required; blob root %s)\n", cfg.httpAddr, root)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// withObs wraps next with attempt/success/error counters + a duration_ms
// observation, named <metricBase>.attempt / .success / .error / .duration_ms.
// A nil observer skips the instrumentation cleanly (used by tests that
// don't wire obs).
//
// R0.23 F4 fix: HTTP routes used to bypass the MemObserver entirely, so
// box_observability only showed MCP-tool traffic. With this middleware
// uploads and one-shot downloads land in the same snapshot.
func withObs(observer obs.Observer, metricBase string, next http.Handler) http.Handler {
	if observer == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tags := map[string]string{"method": r.Method}
		observer.Inc(metricBase+".attempt", tags)
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := float64(time.Since(start).Microseconds()) / 1000.0
		observer.Observe(metricBase+".duration_ms", dur, tags)
		if rec.status >= 400 {
			errTags := map[string]string{"method": r.Method, "status": strconv.Itoa(rec.status)}
			observer.Inc(metricBase+".error", errTags)
		} else {
			observer.Inc(metricBase+".success", tags)
		}
	})
}

// statusRecorder captures the status code so withObs can tag .error vs .success.
// Pass-through on everything else; matches the contract of http.ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// tailscaleNets are the address ranges Tailscale assigns to tailnet devices:
// the IPv4 CGNAT block (100.64.0.0/10) and the IPv6 ULA block
// (fd7a:115c:a1e0::/48). A request whose source IP falls in either range
// has already been authenticated by Tailscale at the network layer — the
// device is on your tailnet because you logged into your Tailscale account
// and authorised it. R7 trust-tailnet mode treats that as sufficient and
// skips the Bearer check, eliminating per-call token friction inside the
// tailnet (the actual work-blocking pain point).
var tailscaleNets = func() []*net.IPNet {
	out := []*net.IPNet{}
	for _, cidr := range []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isTailnetSource reports whether remoteAddr (host:port form from
// http.Request.RemoteAddr) is a Tailscale tailnet address. Direct
// connections (the only mode trust-tailnet is meant for) carry the real
// peer IP here. NOTE: do NOT enable trust-tailnet behind an L7 proxy that
// rewrites RemoteAddr (e.g. Fly's edge) — there the peer is the proxy, not
// the tailnet device, and the check would be meaningless. Fly stays
// Bearer-only.
func isTailnetSource(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr // already bare (some test paths)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range tailscaleNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isLoopback reports whether remoteAddr is the local loopback (127.0.0.0/8 or
// ::1). A request from loopback originates from a process on this very host —
// the operator at the keyboard — so it is at least as trusted as a tailnet
// peer. This lets a local browser open http://127.0.0.1:<port>/dashboard
// token-free (the system HTTP proxy bypasses loopback, so it dodges the
// CGNAT-proxy 502 that hits tailnet IPs). Safe on Fly: external traffic
// arrives via the L7 edge, so RemoteAddr is the proxy, never loopback.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// withDashboardAuth gates ONLY the /dashboard route. It is withBearer plus one
// extra allowance: a loopback source (127.0.0.1/::1) is let through token-free.
// Scope is deliberately narrow — the auth posture of /mcp, /blob, /items is
// unchanged (they keep plain withBearer). The point is purely to let a local
// browser open http://127.0.0.1:<port>/dashboard without a token: the system
// HTTP proxy bypasses loopback, so this dodges the CGNAT-proxy 502 that hits
// tailnet IPs. Safe on Fly: external traffic arrives via the L7 edge, so a
// request's RemoteAddr is the proxy, never loopback.
func withDashboardAuth(trustTailnet bool, want string, next http.Handler) http.Handler {
	bearer := withBearer(trustTailnet, want, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		bearer.ServeHTTP(w, r)
	})
}

// withBearer wraps next with a constant-time Bearer token check. The "Bearer "
// prefix is required; anything else returns 401. Healthz is mounted outside
// this middleware so platforms (fly.io health checks) can probe without
// credentials.
//
// R7: when trustTailnet is true, requests whose source IP is on the
// Tailscale tailnet bypass the Bearer check entirely — Tailscale already
// authenticated the device. Non-tailnet (public) requests still require the
// token, so a single deployment can serve tailnet agents token-free while
// keeping a public Bearer fallback.
func withBearer(trustTailnet bool, want string, next http.Handler) http.Handler {
	const prefix = "Bearer "
	wantBytes := []byte(want)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if trustTailnet && isTailnetSource(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gotBytes := []byte(strings.TrimPrefix(got, prefix))
		if subtle.ConstantTimeCompare(gotBytes, wantBytes) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
