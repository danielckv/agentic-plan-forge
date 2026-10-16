//go:build server && !client

// Command plan-forge-relay hosts remote collaboration sessions. It serves no UI
// and links no repository-analysis code: the relay's dependency closure is
// asserted in CI, so a relay binary can never read a user's source tree.
//
// The HTTP surface in this file is the process shell: flag validation, TLS
// selection, logging and the health endpoint. Session, invite and artifact
// routes are mounted here by internal/relay's hub.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Build metadata, overridden with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

const (
	defaultPort    = 8443
	shutdownGrace  = 10 * time.Second
	readinessDelay = 0
)

type options struct {
	listen       string
	port         int
	tlsCert      string
	tlsKey       string
	authToken    string
	behindProxy  bool
	trustedProxy string
	dataDir      string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan-forge-relay: %v\n", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting plan-forge-relay", "version", version, "commit", commit)

	if err := verifyTokenFilePermissions(logger); err != nil {
		fmt.Fprintf(os.Stderr, "plan-forge-relay: %v\n", err)
		return 1
	}

	addr, err := opts.address()
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan-forge-relay: %v\n", err)
		return 1
	}
	useTLS := opts.tlsCert != "" || opts.tlsKey != ""
	if !useTLS && !opts.behindProxy && !isLoopbackAddr(addr) {
		fmt.Fprintln(os.Stderr, "plan-forge-relay: refusing to serve plaintext off loopback; set --tls-cert and --tls-key, or --behind-proxy with a loopback/private listen address")
		return 1
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           rejectBrowserOrigin(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if useTLS {
			errCh <- srv.ListenAndServeTLS(opts.tlsCert, opts.tlsKey)
			return
		}
		errCh <- srv.ListenAndServe()
	}()
	if readinessDelay > 0 {
		time.Sleep(readinessDelay)
	}
	logger.Info("listening", "addr", addr, "tls", useTLS)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "err", err)
			return 1
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err)
			return 1
		}
	}
	return 0
}

// rejectBrowserOrigin refuses any request that carries an Origin header. The
// relay's clients are local binaries, never browsers, so an Origin is either a
// mistake or an attempt to reach the relay from a page.
func rejectBrowserOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "requests carrying an Origin header are not accepted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var errUsage = errors.New("usage")

func parse(args []string) (options, error) {
	opts := options{port: defaultPort}
	fs := flag.NewFlagSet("plan-forge-relay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.IntVar(&opts.port, "port", defaultPort, "port to listen on")
	fs.StringVar(&opts.listen, "listen", "", "host:port to listen on (overrides --port)")
	fs.StringVar(&opts.tlsCert, "tls-cert", "", "TLS certificate file")
	fs.StringVar(&opts.tlsKey, "tls-key", "", "TLS private key file")
	fs.StringVar(&opts.authToken, "auth-token", "", "deprecated: pass the token with --auth-token-file or PLAN_FORGE_RELAY_TOKEN")
	fs.BoolVar(&opts.behindProxy, "behind-proxy", false, "serve behind a reverse proxy that terminates TLS")
	fs.StringVar(&opts.trustedProxy, "trusted-proxy", "", "CIDR of the reverse proxy whose X-Forwarded-For entry is trusted")
	fs.StringVar(&opts.dataDir, "data-dir", "", "directory for the optional relay write-ahead log")
	if err := fs.Parse(args); err != nil {
		return options{}, errUsage
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("%w: unexpected arguments %v", errUsage, fs.Args())
	}
	// A token on the command line is visible to every local user through the
	// process listing, so the flag is refused with an explanation rather than
	// silently ignored.
	if opts.authToken != "" {
		return options{}, fmt.Errorf("--auth-token is not accepted: command-line values are visible in process listings; use --auth-token-file or PLAN_FORGE_RELAY_TOKEN")
	}
	if (opts.tlsCert == "") != (opts.tlsKey == "") {
		return options{}, errors.New("--tls-cert and --tls-key must be provided together")
	}
	if opts.port < 1 || opts.port > 65535 {
		return options{}, fmt.Errorf("--port must be between 1 and 65535, got %d", opts.port)
	}
	return opts, nil
}

func (o options) address() (string, error) {
	if o.listen != "" {
		if _, _, err := net.SplitHostPort(o.listen); err != nil {
			return "", fmt.Errorf("--listen %q is not host:port: %w", o.listen, err)
		}
		return o.listen, nil
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(o.port)), nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// verifyTokenFilePermissions refuses a token file that other local users can
// read. The token creates sessions, so its file is as sensitive as a password.
func verifyTokenFilePermissions(logger *slog.Logger) error {
	path := os.Getenv("PLAN_FORGE_RELAY_TOKEN_FILE")
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read relay token file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("relay token file %s is readable by other users (mode %s); run chmod 600", path, info.Mode().Perm())
	}
	if strings.TrimSpace(os.Getenv("PLAN_FORGE_RELAY_TOKEN")) == "" {
		logger.Info("relay token file accepted", "path", path)
	}
	return nil
}
