// Package config loads node configuration from a YAML file, with env overrides
// for secrets.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string   `yaml:"listen"`     // e.g. ":3000"
	PublicURL string   `yaml:"public_url"` // e.g. "https://brs.degmods.com"
	DataDir   string   `yaml:"data_dir"`   // where the identity key lives
	Backend   string   `yaml:"backend"`    // storage backend: "r2" (default) | "s3" | "disk"
	R2        R2       `yaml:"r2"`
	S3        S3       `yaml:"s3"`
	Disk      Disk     `yaml:"disk"`
	Upload    Upload   `yaml:"upload"`
	Relay     Relay    `yaml:"relay"`
	Download  Download `yaml:"download"`
	Announce  Announce `yaml:"announce"`
	Ingest    Ingest   `yaml:"ingest"`
	Log       Log      `yaml:"log"`
}

// Ingest subscribes to other relays and stores every mod event they carry, so this
// node becomes a comprehensive mirror/DB of all mods across the network.
type Ingest struct {
	Enabled bool     `yaml:"enabled"`
	Relays  []string `yaml:"relays"` // relays to pull mod events from
}

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`  // debug | info (default) | warn | error
	Format string `yaml:"format"` // text (default) | json
}

// Announce controls the node's self-discovery announcement over Nostr, so other
// nodes and clients can find it and learn what it accepts (federation).
type Announce struct {
	Enabled       bool     `yaml:"enabled"`
	Relays        []string `yaml:"relays"`         // relays to publish the announcement to
	IntervalHours int      `yaml:"interval_hours"` // republish cadence in hours (default 168 = weekly)
}

type Download struct {
	PoWDifficulty   int    `yaml:"pow_difficulty"`    // BUD-POW leading-zero bits; 0 = gate off
	ChallengeTTL    int    `yaml:"challenge_ttl"`     // seconds a challenge stays valid (default 600)
	TrustedIPHeader string `yaml:"trusted_ip_header"` // e.g. "CF-Connecting-IP" or "X-Forwarded-For"; empty = socket IP
}

type Relay struct {
	AdminNpub   string `yaml:"admin_npub"`    // npub allowed to use the NIP-86 management API
	MinEventPoW int    `yaml:"min_event_pow"` // NIP-13 bits required on mod events (legacy 30402 exempt)
	// AcceptAllKinds drops the mod-scoped kind allowlist: any kind, from anyone.
	// For a fork that wants a general-purpose relay rather than a mod one. The
	// remaining protections (banned pubkeys, event takedowns, honored deletions,
	// and min_event_pow) still apply.
	AcceptAllKinds bool `yaml:"accept_all_kinds"`
}

type Upload struct {
	MaxSizeMB         int    `yaml:"max_size_mb"`          // hard cap per blob (default 500)
	MaxConcurrent     int    `yaml:"max_concurrent"`       // global simultaneous uploads (default 4)
	TempDir           string `yaml:"temp_dir"`             // temp spool dir (default: OS temp)
	MinPoW            int    `yaml:"min_pow"`              // NIP-13 bits required on the 24242 auth event (0 = off)
	MinFreeDiskMB     int64  `yaml:"min_free_disk_mb"`     // refuse uploads below this free space (default 1024)
	IdleTimeoutSec    int    `yaml:"idle_timeout_sec"`     // abort an upload after this many seconds with no data (default 60)
	MinUploadRateKBps int    `yaml:"min_upload_rate_kbps"` // abort an upload averaging below this over a 5s window (default 50; negative = off)
	// Accepted file extensions, detected by magic bytes (default ["zip"]). Use
	// ["*"] to allow any type. Recognised: zip, rar, 7z, gz, tar, exe (others → bin).
	AllowedTypes []string `yaml:"allowed_types"`
}

type R2 struct {
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
}

// S3 configures a self-hosted S3-compatible backend (Garage, Ceph, SeaweedFS, …).
// Like R2 it is content-addressed; most self-hosted servers want path-style URLs.
type S3 struct {
	Endpoint  string `yaml:"endpoint"` // host[:port], no scheme
	Region    string `yaml:"region"`   // default "us-east-1"
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
	PathStyle bool   `yaml:"path_style"` // most self-hosted S3 servers need this
}

// Disk configures the local-disk backend (own hardware, no S3 at all).
type Disk struct {
	Path string `yaml:"path"` // blob root; default "<data_dir>/blobs"
}

// Load reads and validates config. Env vars R2_ACCESS_KEY / R2_SECRET_KEY
// override the file so secrets can stay out of it.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}

	if c.Listen == "" {
		c.Listen = ":3000"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Backend == "" {
		c.Backend = "r2"
	}
	if c.R2.Region == "" {
		c.R2.Region = "auto"
	}
	if c.S3.Region == "" {
		c.S3.Region = "us-east-1"
	}
	if c.Disk.Path == "" {
		c.Disk.Path = filepath.Join(c.DataDir, "blobs")
	}
	if c.Upload.MaxSizeMB == 0 {
		c.Upload.MaxSizeMB = 500
	}
	if c.Upload.MaxConcurrent == 0 {
		c.Upload.MaxConcurrent = 4
	}
	if c.Upload.TempDir == "" {
		c.Upload.TempDir = os.TempDir()
	}
	if c.Upload.MinFreeDiskMB == 0 {
		c.Upload.MinFreeDiskMB = 1024
	}
	if c.Upload.IdleTimeoutSec == 0 {
		c.Upload.IdleTimeoutSec = 60
	}
	if c.Upload.MinUploadRateKBps == 0 {
		c.Upload.MinUploadRateKBps = 50
	}
	if len(c.Upload.AllowedTypes) == 0 {
		c.Upload.AllowedTypes = []string{"zip"}
	}
	if c.Download.ChallengeTTL == 0 {
		c.Download.ChallengeTTL = 600
	}
	if c.Announce.IntervalHours == 0 {
		c.Announce.IntervalHours = 168 // weekly
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if v := os.Getenv("R2_ACCESS_KEY"); v != "" {
		c.R2.AccessKey = v
	}
	if v := os.Getenv("R2_SECRET_KEY"); v != "" {
		c.R2.SecretKey = v
	}
	if v := os.Getenv("S3_ACCESS_KEY"); v != "" {
		c.S3.AccessKey = v
	}
	if v := os.Getenv("S3_SECRET_KEY"); v != "" {
		c.S3.SecretKey = v
	}

	if c.PublicURL == "" {
		return nil, fmt.Errorf("config: public_url required")
	}
	switch c.Backend {
	case "r2":
		if c.R2.Endpoint == "" || c.R2.Bucket == "" {
			return nil, fmt.Errorf("config: r2.endpoint and r2.bucket required")
		}
		if c.R2.AccessKey == "" || c.R2.SecretKey == "" {
			return nil, fmt.Errorf("config: r2 credentials required (set in config or R2_ACCESS_KEY/R2_SECRET_KEY)")
		}
	case "s3":
		if c.S3.Endpoint == "" || c.S3.Bucket == "" {
			return nil, fmt.Errorf("config: s3.endpoint and s3.bucket required")
		}
		if c.S3.AccessKey == "" || c.S3.SecretKey == "" {
			return nil, fmt.Errorf("config: s3 credentials required (set in config or S3_ACCESS_KEY/S3_SECRET_KEY)")
		}
	case "disk":
		// disk.path is defaulted above; nothing else required.
	default:
		return nil, fmt.Errorf("config: unknown backend %q (want r2, s3, or disk)", c.Backend)
	}
	return &c, nil
}
