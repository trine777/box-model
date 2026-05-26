package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runHTTP serves the MCP server over Streamable-HTTP at cfg.httpAddr. It
// refuses to start without $BOX_API_TOKEN — remote MCP without auth would
// leak the entire KM repo to the public internet.
//
// The single server instance is shared across all incoming requests; the SDK's
// StreamableHTTPHandler manages per-connection sessions on its own.
func runHTTP(ctx context.Context, cfg config, srv *mcp.Server, stderr io.Writer) error {
	token := os.Getenv("BOX_API_TOKEN")
	if token == "" {
		return fmt.Errorf("BOX_API_TOKEN env required for --http mode (refusing to serve unauthenticated)")
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	authed := withBearer(token, mcpHandler)

	mux := http.NewServeMux()
	mux.Handle("/mcp", authed)
	mux.Handle("/mcp/", authed) // trailing-slash variant some clients send
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
		fmt.Fprintf(stderr, "box-mcp listening on %s (Streamable-HTTP /mcp; Bearer required)\n", cfg.httpAddr)
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

// withBearer wraps next with a constant-time Bearer token check. The "Bearer "
// prefix is required; anything else returns 401. Healthz is mounted outside
// this middleware so platforms (fly.io health checks) can probe without
// credentials.
func withBearer(want string, next http.Handler) http.Handler {
	const prefix = "Bearer "
	wantBytes := []byte(want)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
