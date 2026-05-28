package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
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
	authedMCP := withBearer(token, mcpHandler)

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
	authedBlob := withBearer(token, withObs(observer, "http.blob", blobMux))

	// R0.19: /items/<id>/blob is the one-shot download path for external
	// callers that hold only an item id. Same Bearer middleware.
	itemMux := http.NewServeMux()
	registerItemBlobRoute(itemMux, svc, root, defaultCaller)
	authedItem := withBearer(token, withObs(observer, "http.items_blob", itemMux))

	mux := http.NewServeMux()
	mux.Handle("/mcp", authedMCP)
	mux.Handle("/mcp/", authedMCP) // trailing-slash variant some clients send
	mux.Handle("/blob/", authedBlob)
	mux.Handle("/items/", authedItem)
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
