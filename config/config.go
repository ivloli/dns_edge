package config

import "time"

// Config holds all runtime configuration for dns-edge.
// Populated by ParseFile from a Corefile-style config.
type Config struct {
	Listen  string // DNS listen address, e.g. ":5300"
	Workers int    // SO_REUSEPORT worker count; 0 = disabled (Phase 1 default)
	TCP     bool   // also start a TCP listener
	EDNS0   bool   // advertise EDNS0 in responses (Phase 6)

	API       APIConfig
	PG        PGConfig
	Nacos     NacosConfig
	Sync      SyncConfig
	Geo       GeoConfig
	EdgeAgent EdgeAgentConfig
}

// DefaultXDBFilename is the ip2region xdb filename used when a "geo" block
// is present but doesn't set "xdb" explicitly. Left as a bare relative name
// (resolved against the process's working directory) so it lands under
// whatever data directory the deployment already uses — mirrors GoEdge's own
// Tea.Root/data/ convention; dns-edge's systemd unit sets WorkingDirectory to
// its dedicated data dir for exactly this purpose. Customers installing
// dns-edge never need to type a path for this.
const DefaultXDBFilename = "ip2region.xdb"

// DefaultXDBV6Filename is the ip2region IPv6 xdb filename used when a "geo"
// block is present but doesn't set "xdb_v6" explicitly — the v6 counterpart
// of DefaultXDBFilename. An xdb.Searcher only serves the IP version it was
// built from, so v4 and v6 always need separate files.
const DefaultXDBV6Filename = "ip2region_v6.xdb"

// GeoConfig holds settings for ip2region-based geo-routing.
type GeoConfig struct {
	// XDBPath is the path to the ip2region IPv4 .xdb database file.
	// Empty = geo-routing disabled (no "geo" block at all in the Corefile).
	// When a "geo" block IS present but doesn't set "xdb", this defaults to
	// DefaultXDBFilename — see parseGeo.
	XDBPath string

	// XDBPathV6 is the path to the ip2region IPv6 .xdb database file.
	// When a "geo" block IS present but doesn't set "xdb_v6", this defaults
	// to DefaultXDBV6Filename — see parseGeo.
	XDBPathV6 string

	// AutoUpdate enables periodic xdb refresh.
	AutoUpdate bool

	// Source selects where AutoUpdate fetches a new xdb from:
	//   "api"    (default, recommended) — pull from edgeapi via the same
	//            gRPC connection edgeagent already maintains. Works in
	//            customer deployments with no outbound internet access;
	//            requires an admin to have uploaded+activated an ip2region
	//            artifact in EdgeAdmin (系统设置 → IP2Region 库).
	//   "github" — download directly from ip2region's GitHub Releases.
	//            Only useful for internal dev/test boxes with GitHub access.
	Source string

	// UpdateInterval between update checks. Defaults to 24h.
	UpdateInterval time.Duration

	// GithubToken is an optional personal access token to avoid API rate
	// limits. Only used when Source == "github".
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

// EdgeAgentConfig holds settings for the edgeapi gRPC agent (NS task consumer).
type EdgeAgentConfig struct {
	// Endpoint is the edgeapi gRPC address, e.g. "127.0.0.1:8031".
	// Empty = agent disabled.
	Endpoint string

	// UniqueID is the NSNode.UniqueId from edgeapi's DB.
	UniqueID string

	// Secret is the NSNode.Secret from edgeapi's DB.
	Secret string
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
