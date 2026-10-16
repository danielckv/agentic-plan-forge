//go:build server && !client

// Command plan-forge-relay hosts remote collaboration sessions. It serves no UI
// and links no repository-analysis code: the relay's dependency closure is
// asserted in CI, so a relay binary can never read a user's source tree.
//
// The HTTP surface in this file is the process shell: flag validation, TLS
// selection, logging and the health endpoint. Session, invite, artifact and
// WebSocket routes are mounted here from internal/relay's hub.
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
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
	listen        string
	port          int
	tlsCert       string
	tlsKey        string
	authToken     string
	authTokenFile string
	behindProxy   bool
	trustedProxy  string
	dataDir       string
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

	var trusted *net.IPNet
	if opts.trustedProxy != "" {
		_, trusted, err = net.ParseCIDR(opts.trustedProxy)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan-forge-relay: --trusted-proxy %q is not a CIDR: %v\n", opts.trustedProxy, err)
			return 2
		}
	}
	// X-Forwarded-For is attacker-controlled unless a proxy we trust wrote it,
	// so --behind-proxy needs either a proxy-only listen address or a CIDR.
	if opts.behindProxy && trusted == nil && !isLoopbackOrPrivateAddr(addr) {
		fmt.Fprintln(os.Stderr, "plan-forge-relay: --behind-proxy needs a loopback/private --listen address or --trusted-proxy CIDR")
		return 1
	}

	token, err := loadToken(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan-forge-relay: %v\n", err)
		return 1
	}
	if token == "" {
		if !isLoopbackAddr(addr) {
			fmt.Fprintln(os.Stderr, "plan-forge-relay: a relay token is required off loopback; use --auth-token-file or PLAN_FORGE_RELAY_TOKEN")
			return 1
		}
		logger.Warn("no relay token configured; session creation is open on this loopback listener")
	}

	hub := relay.NewHub(relay.HubConfig{
		Token:        token,
		BehindProxy:  opts.behindProxy,
		TrustedProxy: trusted,
		Logger:       logger,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	hub.Routes(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           relay.RejectBrowserOrigin(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hubCtx, cancelHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		hub.Run(hubCtx)
	}()

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

	code := 0
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "err", err)
			code = 1
		}
	case <-ctx.Done():
		// Closing the hub first ends WebSocket connections: http.Server.Shutdown
		// does not wait for hijacked connections, so they would otherwise be
		// cut when the process exits.
		cancelHub()
		<-hubDone
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err)
			code = 1
		}
	}
	cancelHub()
	<-hubDone
	return code
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
	fs.StringVar(&opts.authToken, "auth-token", "", "not accepted: pass the token with --auth-token-file or PLAN_FORGE_RELAY_TOKEN")
	fs.StringVar(&opts.authTokenFile, "auth-token-file", "", "file holding the relay token (mode 0600 on Unix)")
	fs.BoolVar(&opts.behindProxy, "behind-proxy", false, "serve behind a reverse proxy that terminates TLS")
	fs.StringVar(&opts.trustedProxy, "trusted-proxy", "", "CIDR of the reverse proxy whose X-Forwarded-For entry is trusted")
	fs.StringVar(&opts.dataDir, "data-dir", "", "reserved: relay state is in memory in this build")
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
		return options{}, fmt.Errorf("%w: --auth-token is not accepted: command-line values are visible in process listings; use --auth-token-file or PLAN_FORGE_RELAY_TOKEN", errUsage)
	}
	if opts.dataDir != "" {
		// The relay keeps rooms in memory. Accepting --data-dir and then not
		// persisting would let an operator believe sessions survive a restart.
		return options{}, fmt.Errorf("%w: --data-dir is not supported: rooms are held in memory and the owner's local state reseeds a room after a relay restart", errUsage)
	}
	if (opts.tlsCert == "") != (opts.tlsKey == "") {
		return options{}, fmt.Errorf("%w: --tls-cert and --tls-key must be provided together", errUsage)
	}
	if opts.trustedProxy != "" && !opts.behindProxy {
		return options{}, fmt.Errorf("%w: --trusted-proxy requires --behind-proxy", errUsage)
	}
	if opts.port < 1 || opts.port > 65535 {
		return options{}, fmt.Errorf("%w: --port must be between 1 and 65535, got %d", errUsage, opts.port)
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

func isLoopbackOrPrivateAddr(addr string) bool {
	if isLoopbackAddr(addr) {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsPrivate()
}

// loadToken reads the relay credential from --auth-token-file (or the
// PLAN_FORGE_RELAY_TOKEN_FILE alias), otherwise from PLAN_FORGE_RELAY_TOKEN.
// The token creates sessions, so a file that other local users can read is
// refused.
func loadToken(opts options) (string, error) {
	path := opts.authTokenFile
	if path == "" {
		path = os.Getenv("PLAN_FORGE_RELAY_TOKEN_FILE")
	}
	if path == "" {
		return strings.TrimSpace(os.Getenv("PLAN_FORGE_RELAY_TOKEN")), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read relay token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("relay token file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("relay token file %s is readable by other users (mode %s); run chmod 600", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read relay token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("relay token file %s is empty", path)
	}
	return token, nil
}
