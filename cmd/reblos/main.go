// Command reblos runs a REBLOS node: a Blossom blob server + a Nostr relay,
// with pluggable storage and an optional PoW download gate.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Freakoverse/REBLOS/internal/announce"
	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/Freakoverse/REBLOS/internal/identity"
	"github.com/Freakoverse/REBLOS/internal/server"
	"github.com/Freakoverse/REBLOS/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	setupLogging(cfg)

	id, err := identity.Load(cfg.DataDir)
	if err != nil {
		slog.Error("identity load failed", "err", err)
		os.Exit(1)
	}
	if id.Created {
		fmt.Printf("\n  ► generated node identity\n    npub: %s\n    nsec saved to %s/identity.key\n\n", id.Npub, cfg.DataDir)
	} else {
		fmt.Printf("  node identity npub: %s\n", id.Npub)
	}

	st, err := newStorage(cfg)
	if err != nil {
		slog.Error("storage init failed", "backend", cfg.Backend, "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, st, id.SecretKey)
	if err != nil {
		slog.Error("server init failed", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Shared context for background workers (announce + sync).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Federation: announce this node's capabilities over Nostr (opt-in).
	if cfg.Announce.Enabled && len(cfg.Announce.Relays) > 0 {
		pub := announce.New(announce.Node{
			URL:         cfg.PublicURL,
			Blossom:     true,
			Relay:       true,
			MaxUploadMB: cfg.Upload.MaxSizeMB,
			Kinds:       srv.AcceptedKinds(),
			Gates:       announce.Gates{Pow: cfg.Download.PoWDifficulty},
			Version:     1,
		}, id.SecretKey, cfg.Announce.Relays, time.Duration(cfg.Announce.IntervalHours)*time.Hour)
		go pub.Run(ctx)
		slog.Info("announce enabled", "relays", len(cfg.Announce.Relays), "interval_hours", cfg.Announce.IntervalHours)
	}

	// Per-hub sync watcher (§6): on a timer, re-pull each whitelisted hub's 36942 +
	// member content + blobs from the hub's own relay hints and the operator's sync
	// relays, member-gated, so nothing is lost on downtime. First pass per hub does a
	// deep historical back-fill; later passes catch up on recent events.
	go srv.RunHubRefresh(ctx, time.Duration(cfg.Sync.IntervalMinutes)*time.Minute)

	// Prune spent NIP-09 deletion events (their effect is persisted separately).
	go srv.RunDeletionSweep(ctx)

	slog.Info("node listening",
		"addr", cfg.Listen, "storage", cfg.Backend, "public_url", cfg.PublicURL,
		"pow_bits", cfg.Download.PoWDifficulty)
	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: srv.Handler(),
		// Bound the handshake/headers and idle keep-alives, but NOT total read/write
		// time: large uploads/downloads are legitimately long-running. Per-request
		// upload idle timeouts are enforced in the streaming handler.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		slog.Error("http server stopped", "err", err)
		os.Exit(1)
	}
}

// setupLogging installs the process-wide structured logger from config.
func setupLogging(c *config.Config) {
	var level slog.Level
	switch strings.ToLower(c.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(c.Log.Format) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// newStorage builds the blob backend selected by cfg.Backend. When a read-fallback
// backend is configured (storage re-routing), it wraps the primary and the fallback
// in a storage.Multi: writes go to the primary, reads fall through to the fallback.
func newStorage(cfg *config.Config) (storage.Storage, error) {
	primary, err := buildBackend(cfg.Backend, cfg.R2, cfg.S3, cfg.Disk)
	if err != nil {
		return nil, err
	}
	if cfg.Fallback.Backend == "" {
		return primary, nil
	}
	fb, err := buildBackend(cfg.Fallback.Backend, cfg.Fallback.R2, cfg.Fallback.S3, cfg.Fallback.Disk)
	if err != nil {
		return nil, fmt.Errorf("fallback backend: %w", err)
	}
	slog.Info("storage read-fallback enabled", "primary", cfg.Backend, "fallback", cfg.Fallback.Backend)
	return storage.NewMulti(primary, fb), nil
}

// buildBackend constructs one backend by its kind.
func buildBackend(backend string, r2 config.R2, s3 config.S3, disk config.Disk) (storage.Storage, error) {
	switch backend {
	case "s3":
		return storage.NewR2(storage.R2Config{
			Endpoint:  s3.Endpoint,
			Region:    s3.Region,
			Bucket:    s3.Bucket,
			AccessKey: s3.AccessKey,
			SecretKey: s3.SecretKey,
			UseSSL:    s3.UseSSL,
			PathStyle: s3.PathStyle,
		})
	case "disk":
		return storage.NewDisk(disk.Path)
	case "r2", "":
		return storage.NewR2(storage.R2Config{
			Endpoint:  r2.Endpoint,
			Region:    r2.Region,
			Bucket:    r2.Bucket,
			AccessKey: r2.AccessKey,
			SecretKey: r2.SecretKey,
			UseSSL:    r2.UseSSL,
		})
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}
