package config

import "time"

// Config holds all runtime configuration for dns-edge.
// Populated by ParseFile from a Corefile-style config.
type Config struct {
	Listen  string // DNS listen address, e.g. ":5300"
	Workers int    // SO_REUSEPORT worker count; 0 = disabled (Phase 1 default)
	TCP     bool   // also start a TCP listener
	EDNS0   bool   // advertise EDNS0 in responses (Phase 6)

	API   APIConfig
	PG    PGConfig
	Nacos NacosConfig
	Sync  SyncConfig
	Geo   GeoConfig
}

// GeoConfig holds settings for ip2region-based geo-routing.
type GeoConfig struct {
	// XDBPath is the path to the ip2region .xdb database file.
	// Empty = geo-routing disabled.
	XDBPath string

	// AutoUpdate enables periodic xdb refresh from GitHub Releases.
	AutoUpdate bool

	// UpdateInterval between release checks. Defaults to 24h.
	UpdateInterval time.Duration

	// GithubToken is an optional personal access token to avoid API rate limits.
	GithubToken string
}

type APIConfig struct {
	Listen       string // HTTP API listen address, e.g. ":8080"
	GoEdgeSecret string // shared secret for GoEdge customHTTP provider; empty = auth disabled

	// EdgeDNS API (edgeDNSAPI provider) credentials.
	// When both are set, GoEdge can use dns-edge as a native EdgeDNS node.
	// Empty = edgeDNSAPI disabled.
	EdgeDNSKeyID     string
	EdgeDNSKeySecret string
}

type PGConfig struct {
	DSN string // PostgreSQL connection string
}

type NacosConfig struct {
	Addr         string // host:port of Nacos server
	Namespace    string // Nacos namespace ID; empty = public
	Group        string // Nacos group; empty defaults to DEFAULT_GROUP
	DataIDPrefix string // DataID prefix; default "dns_weights:"
	Username     string // optional Nacos auth
	Password     string // optional Nacos auth
}

type SyncConfig struct {
	Interval  time.Duration // timer-based sync interval; default 30s
	Prob      float64       // per-query probabilistic sync probability; default 0.01
	RateLimit int           // max probabilistic syncs per second (Token Bucket); default 100
}

// Defaults returns a Config with safe production defaults.
func Defaults() *Config {
	return &Config{
		Listen:  ":5300",
		Workers: 0,
		TCP:     true,
		EDNS0:   true,
		API: APIConfig{
			Listen: ":8080",
		},
		Nacos: NacosConfig{
			Group:        "DEFAULT_GROUP",
			DataIDPrefix: "dns_weights:",
		},
		Sync: SyncConfig{
			Interval:  30 * time.Second,
			Prob:      0.01,
			RateLimit: 100,
		},
	}
}
