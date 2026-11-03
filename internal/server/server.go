// Package server wires the khatru relay + Blossom handler to the storage backend.
//
//   - Blossom: GET/HEAD/list/delete from R2 via khatru; a streaming PUT/HEAD
//     /upload handler sits in front of the relay (khatru's buffered upload is
//     unusable for 500 MB files).
//   - Relay: hub-scoped (NIP-CHAT member-gated kinds), persisted in an embedded
//     badger event store, with NIP-86 admin.
package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Freakoverse/REBLOS/dashboard"
	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/Freakoverse/REBLOS/internal/storage"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/blossom"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type Server struct {
	relay   *khatru.Relay
	bl      *blossom.BlossomServer
	handler http.Handler
	store   *badger.BadgerBackend

	storage            storage.Storage
	publicURL          string
	tempDir            string
	dataDir            string
	maxUploadBytes     int64
	minFreeDiskMB      int64
	uploadIdleTimeout  time.Duration
	minUploadRateBps   int64 // 0 = off
	minUploadPoW       int
	allowedUploadTypes []string // accepted extensions (magic-detected); "*" = any
	minEventPoW        int
	acceptAllKinds     bool  // fork switch: no kind allowlist + no member gate (see config.Relay)
	extraKinds         []int // additional accepted kinds beyond the NIP-CHAT hub set
	maxConcurrent      int   // global simultaneous uploads (display-only mirror of the limiter's cap)
	limiter            *uploadLimiter
	block              *blocklist
	white              *whitelist
	bannedEv           *bannedEvents
	deletions          *deletions         // author-initiated NIP-09 takedowns (persistent)
	owners             *blobOwners        // hash -> uploader pubkey (persistent)
	refs               *blobRefs          // hash -> mods that reference it (in-memory, dashboard labelling)
	caps               *uploadCaps        // per-upload size caps, runtime-adjustable (persistent)
	blacklist          *blacklistedHashes // blob hashes barred from upload/download (persistent)
	quarantine         *quarantine        // blobs withheld from download (reversible; persistent)
	adminPubkey        string             // hex; the only key allowed to delete blobs (moderation)

	// admin blob-listing cache (avoids a full storage scan on every page click)
	blobCacheMu sync.Mutex
	blobCache   []storage.BlobInfo
	blobCacheAt time.Time

	// download gate (BUD-POW)
	powDifficulty   int
	challengeTTL    time.Duration
	trustedIPHeader string
	gateKey         []byte

	// NIP-CHAT hubs: whitelist-by-coordinate (persistent) + the derived per-hub
	// poster-set cache (in-memory, rebuilt from the member tree on each revision).
	hubs    *hub.Registry
	posters *hub.PosterCache

	// Durability sync (§6): the operator-managed general relay list the per-hub
	// watcher pulls from (on top of each hub's own hints), plus a once-per-process
	// guard so the deep historical back-fill runs only on a hub's first refresh.
	syncR      *syncRelays
	backfillMu sync.Mutex
	backfilled map[string]bool

	// Storage re-routing (§4): the Multi backend (non-nil only when a read-fallback
	// is configured) and the migration tracker for copying the fallback's backlog
	// into the primary.
	storageMulti    *storage.Multi
	migration       *migrationState
	storagePrimary  backendDesc
	storageFallback *backendDesc // nil when no read-fallback is configured
}

// New builds the node's HTTP handler (streaming upload + blossom + relay).
// gateSecret seeds the stateless challenge HMAC key (the node's secret key).
func New(cfg *config.Config, st storage.Storage, gateSecret string) (*Server, error) {
	relay := khatru.NewRelay()

	// srv is captured by the LoadBlob closure below; it is assigned once the Server
	// is built, well before any request is served, so the closure sees a live value.
	var srv *Server

	bl := blossom.New(relay, cfg.PublicURL)
	bl.Store = &r2Index{st: st, publicURL: cfg.PublicURL}
	bl.LoadBlob = append(bl.LoadBlob, func(ctx context.Context, sha256, ext string) (io.ReadSeeker, error) {
		rc, err := st.Load(ctx, sha256, ext)
		if err == nil {
			return rc, nil // *minio.Object is also an io.Closer; khatru closes ReadSeekers that implement it
		}
		// Local miss: lazily mirror the blob from the whitelisted hubs' Blossom
		// servers (media durability, §6), then serve it. Falls back to the original
		// not-found error when there is nothing to mirror from or the pull fails.
		if srv != nil {
			if mrc, mErr := srv.mirrorOnRead(ctx, sha256, ext); mErr == nil {
				return mrc, nil
			}
		}
		return nil, err
	})
	bl.DeleteBlob = append(bl.DeleteBlob, func(ctx context.Context, sha256, ext string) error {
		return st.Delete(ctx, sha256, ext)
	})
	// Disable khatru's own (buffered) upload — we serve /upload ourselves.
	bl.RejectUpload = append(bl.RejectUpload, func(ctx context.Context, auth *nostr.Event, size int, ext string) (bool, string, int) {
		return true, "use this server's streaming upload endpoint", http.StatusServiceUnavailable
	})

	// Embedded persistent event store for the mod relay.
	store := &badger.BadgerBackend{Path: filepath.Join(cfg.DataDir, "events")}
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("event store: %w", err)
	}

	s := &Server{
		relay:              relay,
		bl:                 bl,
		store:              store,
		storage:            st,
		publicURL:          cfg.PublicURL,
		tempDir:            cfg.Upload.TempDir,
		dataDir:            cfg.DataDir,
		maxUploadBytes:     int64(cfg.Upload.MaxSizeMB) * 1024 * 1024,
		minFreeDiskMB:      cfg.Upload.MinFreeDiskMB,
		uploadIdleTimeout:  time.Duration(cfg.Upload.IdleTimeoutSec) * time.Second,
		minUploadRateBps:   int64(maxInt(cfg.Upload.MinUploadRateKBps, 0)) * 1024,
		minUploadPoW:       cfg.Upload.MinPoW,
		allowedUploadTypes: cfg.Upload.AllowedTypes,
		minEventPoW:        cfg.Relay.MinEventPoW,
		acceptAllKinds:     cfg.Relay.AcceptAllKinds,
		extraKinds:         cfg.Relay.ExtraKinds,
		maxConcurrent:      cfg.Upload.MaxConcurrent,
		limiter:            newUploadLimiter(cfg.Upload.MaxConcurrent),
		block:              loadBlocklist(filepath.Join(cfg.DataDir, "blocklist.json")),
		white:              loadWhitelist(filepath.Join(cfg.DataDir, "whitelist.json")),
		bannedEv:           loadBannedEvents(filepath.Join(cfg.DataDir, "banned_events.json")),
		deletions:          loadDeletions(filepath.Join(cfg.DataDir, "deletions.json")),
		owners:             loadBlobOwners(filepath.Join(cfg.DataDir, "blob_owners.json")),
		refs:               newBlobRefs(cfg.PublicURL),
		caps: loadUploadCaps(
			filepath.Join(cfg.DataDir, "upload_caps.json"),
			int64(cfg.Upload.MaxSizeMB),   // normal default
			int64(cfg.Upload.MaxSizeMB)*5, // whitelisted default (preserves the old 5× behaviour)
		),
		blacklist:   loadBlacklist(filepath.Join(cfg.DataDir, "blacklist.json")),
		quarantine:  loadQuarantine(filepath.Join(cfg.DataDir, "quarantine.json")),
		adminPubkey: resolvePubkey(cfg.Relay.AdminNpub),

		powDifficulty:   cfg.Download.PoWDifficulty,
		challengeTTL:    time.Duration(cfg.Download.ChallengeTTL) * time.Second,
		trustedIPHeader: cfg.Download.TrustedIPHeader,
		gateKey:         deriveGateKey(gateSecret),
	}
	if len(s.allowedUploadTypes) == 0 {
		s.allowedUploadTypes = config.DefaultUploadTypes
	}
	srv = s // the LoadBlob closure's mirror-on-read fallback can now reach the Server
	if m, ok := st.(*storage.Multi); ok {
		s.storageMulti = m // a read-fallback is configured; migration + status can use it
	}
	s.migration = newMigrationState()
	s.storagePrimary = describeBackend(cfg.Backend, cfg.R2, cfg.S3, cfg.Disk)
	if cfg.Fallback.Backend != "" {
		d := describeBackend(cfg.Fallback.Backend, cfg.Fallback.R2, cfg.Fallback.S3, cfg.Fallback.Disk)
		s.storageFallback = &d
	}

	// Hub registry + poster-set cache. The cache's build fetches the member-tree
	// blobs local-first (our own storage) then from the hub's Blossom hints, with
	// mandatory sha256 verification, and parses the plaintext leaf pages.
	s.syncR = loadSyncRelays(filepath.Join(cfg.DataDir, "sync_relays.json"), config.DefaultSyncRelays, cfg.Sync.ExtraRelays)
	s.backfilled = map[string]bool{}
	s.hubs = hub.LoadRegistry(filepath.Join(cfg.DataDir, "hubs.json"))
	s.posters = hub.NewPosterCache(func(ctx context.Context, indexHash string, remotes []string) (hub.PosterSet, error) {
		// Mirror: a member-tree blob fetched from a hub's remote Blossom is stored
		// locally, so building a poster set also makes the member tree durable here.
		f := &hub.Fetcher{Local: st, Remotes: remotes, Mirror: s.mirrorHubBlob}
		return hub.BuildPosterSet(indexHash, f.Bind(ctx))
	})

	s.setupRelay(store, s.adminPubkey)
	// Label existing blobs against the mods already stored (dashboard only). Runs
	// before we start serving, so no ingest can race it.
	s.backfillRefs(context.Background())
	// Build poster sets for whitelisted hubs whose 36942 event is already stored.
	s.reconcileHubsFromStore(context.Background())

	// Streaming /upload in front; everything else (blossom GET, relay WS, NIP-11,
	// NIP-86) → the relay.
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", s.handleUploadPut)
	mux.HandleFunc("HEAD /upload", s.handleUploadHead)
	mux.HandleFunc("OPTIONS /upload", func(w http.ResponseWriter, r *http.Request) {
		setUploadCORS(w)
		w.WriteHeader(http.StatusNoContent)
	})
	// Claim an already-stored blob as an uploader (dedup skipped the real upload).
	mux.HandleFunc("PUT /claim/{hash}", s.handleClaim)
	mux.HandleFunc("OPTIONS /claim/{hash}", func(w http.ResponseWriter, r *http.Request) {
		setUploadCORS(w)
		w.WriteHeader(http.StatusNoContent)
	})
	// Admin API (NIP-98 auth, admin key). Blob deletion via DELETE /<hash>.
	mux.HandleFunc("DELETE /{hash}", s.handleDelete)
	mux.HandleFunc("GET /admin/config", s.handleAdminConfig)
	mux.HandleFunc("GET /admin/stats", s.handleAdminStats)
	mux.HandleFunc("GET /admin/events", s.handleAdminEvents)
	mux.HandleFunc("GET /admin/blobs", s.handleAdminBlobs)
	mux.HandleFunc("GET /admin/blob/{hash}", s.handleAdminBlobDownload)  // gate-free admin download
	mux.HandleFunc("DELETE /admin/blob/{hash}", s.handleAdminBlobDelete) // NIP-98 admin delete (dashboard)
	mux.HandleFunc("GET /admin/blacklist", s.handleBlacklistList)
	mux.HandleFunc("POST /admin/blacklist", s.handleBlacklistAdd)
	mux.HandleFunc("DELETE /admin/blacklist", s.handleBlacklistRemove)
	mux.HandleFunc("DELETE /admin/quarantine", s.handleQuarantineRelease)
	mux.HandleFunc("GET /admin/whitelist", s.handleAdminWhitelistList)
	mux.HandleFunc("POST /admin/whitelist", s.handleAdminWhitelistAdd)
	mux.HandleFunc("DELETE /admin/whitelist", s.handleAdminWhitelistRemove)
	mux.HandleFunc("PUT /admin/upload-caps", s.handleAdminUploadCapsSet)
	mux.HandleFunc("DELETE /admin/upload-caps", s.handleAdminUploadCapsReset)
	mux.HandleFunc("GET /admin/banned-events", s.handleBannedEventsList)
	mux.HandleFunc("POST /admin/banned-events", s.handleBanEvent)
	mux.HandleFunc("DELETE /admin/banned-events", s.handleUnbanEvent)
	mux.HandleFunc("GET /admin/hubs", s.handleAdminHubsList)
	mux.HandleFunc("POST /admin/hubs", s.handleAdminHubsAdd)
	mux.HandleFunc("DELETE /admin/hubs", s.handleAdminHubsRemove)
	mux.HandleFunc("GET /admin/sync-relays", s.handleAdminSyncRelaysList)
	mux.HandleFunc("POST /admin/sync-relays", s.handleAdminSyncRelaysAdd)
	mux.HandleFunc("PUT /admin/sync-relays", s.handleAdminSyncRelaysSet)
	mux.HandleFunc("DELETE /admin/sync-relays", s.handleAdminSyncRelaysRemove)
	mux.HandleFunc("GET /admin/storage", s.handleAdminStorageGet)
	mux.HandleFunc("POST /admin/storage/migrate", s.handleAdminStorageMigrate)
	mux.HandleFunc("OPTIONS /admin/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		setAdminCORS(w)
		w.WriteHeader(http.StatusNoContent)
	})
	// Admin dashboard (embedded SPA). More specific than "/", so ServeMux routes
	// /dashboard/* here regardless of registration order. The app itself calls
	// the same-origin /admin/* endpoints, which stay NIP-98 admin-gated — the
	// static assets are public, the actions behind them are not.
	if sub, err := fs.Sub(dashboard.Assets, "dist"); err == nil {
		fileServer := http.StripPrefix("/dashboard/", http.FileServerFS(sub))
		mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
		})
		mux.Handle("GET /dashboard/", fileServer)
	}

	mux.Handle("/", s.gate(relay)) // BUD-POW gate on blob GET; pass-through otherwise
	s.handler = mux

	return s, nil
}

func deriveGateKey(secret string) []byte {
	sum := sha256.Sum256([]byte("deg-mods-gate|" + secret))
	return sum[:]
}

// Handler returns the node's combined HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases the event store.
func (s *Server) Close() {
	if s.store != nil {
		s.store.Close()
	}
}

func (s *Server) blocked(pubkey string) bool { return s.block.has(pubkey) }

// resolvePubkey accepts an npub or a hex pubkey and returns hex ("" if empty/bad).
func resolvePubkey(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "npub1") {
		if _, v, err := nip19.Decode(s); err == nil {
			if hex, ok := v.(string); ok {
				return hex
			}
		}
		return ""
	}
	return s
}
