// Package edgeagent connects dns-edge to edgeapi as a gRPC client.
// It polls FindNodeTasks for nsConfigChanged / nsDomainChanged / nsRecordChanged
// and refreshes the in-memory ZoneStore accordingly.
package edgeagent

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"
	"gitlab.gainetics.io/backend-cdn/goedge/edgecommon/pkg/rpc/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	dnshandler "dns-edge/internal/dns"
	"dns-edge/internal/iface"
	"dns-edge/internal/nsroute"
)

// Agent polls edgeapi for NS node tasks and refreshes ZoneStore.
type Agent struct {
	store    iface.ZoneStore
	log      *zap.Logger
	uniqueID string
	secret   string
	endpoint string // host:port of edgeapi gRPC

	clientMu                sync.RWMutex
	ip2RegionArtifactClient pb.IPLibraryArtifactServiceClient
	fileChunkClient         pb.FileChunkServiceClient

	soaMu     sync.RWMutex
	soaConfig *nsClusterSOAConfig // cluster-level SOA config, refreshed via FindCurrentNSNodeConfig; nil until first fetch succeeds

	hostsMu     sync.RWMutex
	hostsConfig []string // cluster-level NS "hosts" setting, refreshed alongside SOA; nil until first fetch succeeds or if never configured

	// tlsCertStore/dohCertStore receive the cluster's TLS(DoT)/DoH certs as
	// refreshNodeConfig fetches them from the same FindCurrentNSNodeConfig
	// blob the SOA config comes from. Always non-nil (main.go constructs
	// them unconditionally, see cmd/dns-edge/main.go) — whether a local
	// listener actually reads from them is a separate, Corefile-only
	// decision this package doesn't need to know about.
	tlsCertStore *dnshandler.CertStore
	dohCertStore *dnshandler.CertStore
	tlsMu        sync.Mutex
	lastTLS      nsClusterCertConfig // last-applied config, to skip redundant CertStore.Set calls
	dohMu        sync.Mutex
	lastDoH      nsClusterCertConfig

	// onIP2RegionChanged, if set via SetIP2RegionChangedHandler, is invoked
	// (in its own goroutine) whenever an nsIP2RegionChanged task arrives —
	// edgeapi broadcasts this after its optional GitHub auto-sync job
	// activates a new ip2region artifact, letting dns-edge refresh well
	// before its own regular ip2region poll interval would have caught it.
	onIP2RegionChanged func()
}

// SetIP2RegionChangedHandler registers a callback invoked whenever edgeapi
// reports that the active ip2region artifact changed. Must be called before
// Run, since Run's poll loop reads it without further synchronization.
func (a *Agent) SetIP2RegionChangedHandler(fn func()) {
	a.onIP2RegionChanged = fn
}

// New creates an Agent. endpoint is the edgeapi gRPC address (e.g. "127.0.0.1:8031").
// uniqueID and secret come from the NSNode row in edgeapi's DB. tlsCertStore/
// dohCertStore receive cert updates as the cluster's TLS/DoH settings change;
// pass freshly-constructed (possibly otherwise-unused) stores even if this
// node has no local tls{}/doh{} listener configured.
func New(endpoint, uniqueID, secret string, store iface.ZoneStore, log *zap.Logger, tlsCertStore, dohCertStore *dnshandler.CertStore) *Agent {
	return &Agent{
		store:        store,
		log:          log,
		uniqueID:     uniqueID,
		secret:       secret,
		endpoint:     endpoint,
		tlsCertStore: tlsCertStore,
		dohCertStore: dohCertStore,
	}
}

// Run starts the polling loop and blocks until ctx is cancelled.
//
// It retries the initial connection with exponential backoff, and relies on
// gRPC's built-in transport reconnection (sped up by keepalive pings) to
// recover from mid-session network drops without restarting the process.
// Node online status is re-reported on every poll tick rather than once at
// startup, so status recovers automatically after a reconnect.
func (a *Agent) Run(ctx context.Context) {
	conn := a.dialWithRetry(ctx)
	if conn == nil {
		return // ctx cancelled before a connection was established
	}
	defer conn.Close()

	go a.watchConnState(ctx, conn)

	taskClient := pb.NewNodeTaskServiceClient(conn)
	domainClient := pb.NewNSDomainServiceClient(conn)
	recordClient := pb.NewNSRecordServiceClient(conn)
	nsNodeClient := pb.NewNSNodeServiceClient(conn)

	a.clientMu.Lock()
	a.ip2RegionArtifactClient = pb.NewIPLibraryArtifactServiceClient(conn)
	a.fileChunkClient = pb.NewFileChunkServiceClient(conn)
	a.clientMu.Unlock()

	a.log.Info("edgeagent: connected to edgeapi", zap.String("endpoint", a.endpoint))
	a.reportStatus(ctx, nsNodeClient, true)

	var domainVersion int64
	var recordVersion int64

	// dns-edge keeps no persistent state, so a freshly started (or just
	// restarted) process always has an empty ZoneStore. NS-mode data only
	// ever transfers in response to nsDomainChanged/nsRecordChanged tasks,
	// and those tasks are one-shot — if they were already consumed before
	// this restart, nothing will ever re-deliver the existing domains and
	// records. Do one unconditional full sync right after connecting so a
	// fresh ZoneStore is always seeded from whatever currently exists in
	// edgeapi, mirroring the zoneCount-based auto-recovery CDN mode already
	// has (there it's edgeapi-initiated since edgeapi pushes; here it has to
	// be agent-initiated since the agent pulls).
	if err := a.syncDomains(ctx, domainClient, &domainVersion); err != nil {
		a.log.Warn("edgeagent: initial domain sync failed", zap.Error(err))
	}
	if err := a.syncRecords(ctx, recordClient, &recordVersion); err != nil {
		a.log.Warn("edgeagent: initial record sync failed", zap.Error(err))
	}
	// Fetch the cluster's SOA/TLS/DoH config before the initial domain sync's
	// zones would otherwise be created with a nil SOA (falling back to
	// dns/handler.go's synthesized default) — order matters here since
	// syncDomains above already ran once; refreshNodeConfig backfills any
	// zone it just created via SetSOA, and gets the TLS/DoH cert stores
	// populated before any DoT/DoH listener receives its first connection.
	if err := a.refreshNodeConfig(ctx, nsNodeClient); err != nil {
		a.log.Warn("edgeagent: initial node config fetch failed", zap.Error(err))
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.reportStatusOnShutdown(nsNodeClient)
			return
		case <-ticker.C:
			a.reportStatus(ctx, nsNodeClient, true)
			a.poll(ctx, taskClient, nsNodeClient, domainClient, recordClient, &domainVersion, &recordVersion)
		}
	}
}

// dialWithRetry dials edgeapi, retrying with exponential backoff (capped at
// 30s) until it succeeds or ctx is cancelled. In practice grpc.DialContext
// rarely errors since it dials lazily, but this guards against the case
// (bad target, resolver issues) where it does — without it, Run would exit
// permanently and never be retried by the caller.
func (a *Agent) dialWithRetry(ctx context.Context) *grpc.ClientConn {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		conn, err := grpc.DialContext(ctx, a.endpoint, //nolint:staticcheck
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithUnaryInterceptor(a.authInterceptor()),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                20 * time.Second,
				Timeout:             10 * time.Second,
				PermitWithoutStream: true,
			}),
		)
		if err == nil {
			return conn
		}
		a.log.Warn("edgeagent: dial failed, retrying",
			zap.String("endpoint", a.endpoint), zap.Error(err), zap.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// watchConnState logs gRPC transport connectivity transitions so reconnect
// events are visible in logs rather than only inferable from RPC errors.
func (a *Agent) watchConnState(ctx context.Context, conn *grpc.ClientConn) {
	last := conn.GetState()
	for {
		if !conn.WaitForStateChange(ctx, last) {
			return // ctx cancelled
		}
		cur := conn.GetState()
		if cur == connectivity.Ready && last != connectivity.Ready {
			a.log.Info("edgeagent: connection to edgeapi (re)established", zap.String("endpoint", a.endpoint))
		} else if cur != connectivity.Ready {
			a.log.Warn("edgeagent: connection to edgeapi not ready", zap.String("state", cur.String()))
		}
		last = cur
	}
}

// reportStatus sends the node's online status to edgeapi.
func (a *Agent) reportStatus(ctx context.Context, client pb.NSNodeServiceClient, isActive bool) {
	statusJSON, _ := json.Marshal(map[string]bool{"isActive": isActive})
	if _, err := client.UpdateNSNodeStatus(ctx, &pb.UpdateNSNodeStatusRequest{StatusJSON: statusJSON}); err != nil {
		a.log.Warn("edgeagent: UpdateNSNodeStatus failed", zap.Error(err))
	}
}

// reportStatusOnShutdown best-effort marks the node offline when Run exits.
// It uses a fresh short-lived context since ctx is already cancelled at this point.
func (a *Agent) reportStatusOnShutdown(client pb.NSNodeServiceClient) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a.reportStatus(shutdownCtx, client, false)
}

// nsClusterSOAConfig mirrors edgeapi's models.NSClusterSOAConfig — field
// names must stay in sync since this is parsed out of a raw JSON blob
// (FindCurrentNSNodeConfig's "soa" key), not a typed proto message.
type nsClusterSOAConfig struct {
	NS      string `json:"ns"`
	Mbox    string `json:"mbox"`
	Serial  uint32 `json:"serial"`
	Refresh uint32 `json:"refresh"`
	Retry   uint32 `json:"retry"`
	Expire  uint32 `json:"expire"`
	MinTTL  uint32 `json:"minttl"`
}

// buildSOA constructs the SOA record for apex's zone from the currently
// cached cluster SOA config. NS/Mbox fall back to "ns1.<apex>"/
// "hostmaster.<apex>" when unset (admin left them blank), matching
// dns/handler.go's syntheticSOA fallback so the two code paths behave
// identically for an unconfigured cluster.
func (a *Agent) buildSOA(apex string) *mdns.SOA {
	a.soaMu.RLock()
	cfg := a.soaConfig
	a.soaMu.RUnlock()

	if cfg == nil {
		cfg = &nsClusterSOAConfig{Serial: 1, Refresh: 3600, Retry: 900, Expire: 604800, MinTTL: 300}
	}
	ns := cfg.NS
	if ns == "" {
		ns = "ns1." + apex
	}
	mbox := cfg.Mbox
	if mbox == "" {
		mbox = "hostmaster." + apex
	}
	return &mdns.SOA{
		Hdr:     mdns.RR_Header{Name: apex, Rrtype: mdns.TypeSOA, Class: mdns.ClassINET, Ttl: cfg.MinTTL},
		Ns:      ns,
		Mbox:    mbox,
		Serial:  cfg.Serial,
		Refresh: cfg.Refresh,
		Retry:   cfg.Retry,
		Expire:  cfg.Expire,
		Minttl:  cfg.MinTTL,
	}
}

// buildNS constructs the zone's NS records from the currently cached cluster
// "hosts" config. Unlike buildSOA there is no synthetic fallback when hosts
// is empty: SOA's Ns/Mbox fields are informational (nobody actually queries
// them as real nameservers), but a zone's NS answer tells real resolvers
// where to send follow-up queries — inventing "ns1.<apex>" would point
// customers at a hostname that doesn't exist and has no address record,
// which is worse than just answering empty. Returns nil (no records) until
// an admin has actually configured hosts for the cluster.
func (a *Agent) buildNS(apex string) []*mdns.NS {
	a.hostsMu.RLock()
	hosts := a.hostsConfig
	a.hostsMu.RUnlock()

	if len(hosts) == 0 {
		return nil
	}

	a.soaMu.RLock()
	ttl := uint32(3600)
	if a.soaConfig != nil && a.soaConfig.MinTTL > 0 {
		ttl = a.soaConfig.MinTTL
	}
	a.soaMu.RUnlock()

	rrs := make([]*mdns.NS, 0, len(hosts))
	for _, host := range hosts {
		if host == "" {
			continue
		}
		rrs = append(rrs, &mdns.NS{
			Hdr: mdns.RR_Header{Name: apex, Rrtype: mdns.TypeNS, Class: mdns.ClassINET, Ttl: ttl},
			Ns:  iface.FQDN(host),
		})
	}
	return rrs
}

// nsClusterCertConfig mirrors edgeapi's composeNSClusterCertPayload output —
// the {isOn, certPEM, keyPEM} shape both the "tls" and "doh" keys in
// FindCurrentNSNodeConfig's JSON blob share. A zero value (IsOn:false) means
// "not configured/disabled", matching CertStore.Set's fail-closed behavior.
// All fields are plain strings/bool so the struct is directly comparable
// with == (used to skip redundant CertStore.Set calls when nothing changed).
type nsClusterCertConfig struct {
	IsOn    bool   `json:"isOn"`
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
}

// refreshNodeConfig fetches the node's cluster-level config (SOA + TLS/DoH
// certs) via a single FindCurrentNSNodeConfig call and applies whichever
// parts changed since the last fetch. This is the sole consumer of that RPC;
// SOA/TLS/DoH are three independent sub-keys in the same JSON blob rather
// than three separate calls, since they're all "cluster settings this node
// needs to react to" and edgeapi already composes them together.
// Errors are logged and swallowed by the caller — a stale/default config is
// not worth failing the whole poll tick over.
func (a *Agent) refreshNodeConfig(ctx context.Context, client pb.NSNodeServiceClient) error {
	resp, err := client.FindCurrentNSNodeConfig(ctx, &pb.FindCurrentNSNodeConfigRequest{})
	if err != nil {
		return fmt.Errorf("FindCurrentNSNodeConfig: %w", err)
	}
	if len(resp.NsNodeJSON) == 0 {
		return nil
	}

	var payload struct {
		SOA   *nsClusterSOAConfig  `json:"soa"`
		Hosts []string             `json:"hosts"`
		TLS   *nsClusterCertConfig `json:"tls"`
		DoH   *nsClusterCertConfig `json:"doh"`
	}
	if err := json.Unmarshal(resp.NsNodeJSON, &payload); err != nil {
		return fmt.Errorf("decode node config: %w", err)
	}

	soaChanged := payload.SOA != nil
	if payload.SOA != nil {
		a.soaMu.Lock()
		unchanged := a.soaConfig != nil && *a.soaConfig == *payload.SOA
		a.soaConfig = payload.SOA
		a.soaMu.Unlock()
		soaChanged = !unchanged
	}

	a.hostsMu.Lock()
	hostsChanged := !slices.Equal(a.hostsConfig, payload.Hosts)
	a.hostsConfig = payload.Hosts
	a.hostsMu.Unlock()

	if soaChanged || hostsChanged {
		for apex := range a.store.Snapshot() {
			if soaChanged {
				_ = a.store.SetSOA(apex, a.buildSOA(apex))
			}
			if hostsChanged {
				_ = a.store.SetNS(apex, a.buildNS(apex))
			}
		}
	}

	if err := a.applyCertConfig(payload.TLS, a.tlsCertStore, &a.tlsMu, &a.lastTLS, "tls"); err != nil {
		return err
	}
	if err := a.applyCertConfig(payload.DoH, a.dohCertStore, &a.dohMu, &a.lastDoH, "doh"); err != nil {
		return err
	}
	return nil
}

// applyCertConfig pushes cfg (a "tls" or "doh" sub-key from the node config
// blob; nil means "key absent from response") into store, skipping the
// CertStore.Set call entirely when it's identical to the last-applied value
// (store.Set itself is cheap, but this avoids re-parsing the PEM on every
// 10s poll tick when nothing changed, which is the overwhelmingly common
// case). A nil/zero-value cfg is treated as "disabled", same as an explicit
// {isOn:false} — both make the CertStore fail closed.
func (a *Agent) applyCertConfig(cfg *nsClusterCertConfig, store *dnshandler.CertStore, mu *sync.Mutex, last *nsClusterCertConfig, label string) error {
	if cfg == nil {
		cfg = &nsClusterCertConfig{}
	}

	mu.Lock()
	unchanged := *last == *cfg
	*last = *cfg
	mu.Unlock()
	if unchanged {
		return nil
	}

	if err := store.Set([]byte(cfg.CertPEM), []byte(cfg.KeyPEM), cfg.IsOn); err != nil {
		return fmt.Errorf("apply %s cert: %w", label, err)
	}
	return nil
}

func (a *Agent) poll(
	ctx context.Context,
	taskClient pb.NodeTaskServiceClient,
	nsNodeClient pb.NSNodeServiceClient,
	domainClient pb.NSDomainServiceClient,
	recordClient pb.NSRecordServiceClient,
	domainVersion, recordVersion *int64,
) {
	resp, err := taskClient.FindNodeTasks(ctx, &pb.FindNodeTasksRequest{Version: 0})
	if err != nil {
		a.log.Warn("edgeagent: FindNodeTasks failed", zap.Error(err))
		return
	}

	for _, task := range resp.NodeTasks {
		var taskErr error
		switch task.Type {
		case "nsConfigChanged":
			// Covers both the node's own config and its cluster's — includes
			// Hosts/SOA/TLS/DoH settings saved in EdgeAdmin's "集群设置" page
			// (NSClusterDAO.NotifyUpdate broadcasts this same task type to
			// the whole cluster). Re-fetch and apply all of them.
			taskErr = a.refreshNodeConfig(ctx, nsNodeClient)
		case "nsDomainChanged":
			taskErr = a.syncDomains(ctx, domainClient, domainVersion)
		case "nsRecordChanged":
			taskErr = a.syncRecords(ctx, recordClient, recordVersion)
		case "nsIP2RegionChanged":
			if a.onIP2RegionChanged != nil {
				go a.onIP2RegionChanged()
			}
			taskErr = nil
		default:
			// Unknown task types are acked as ok to avoid task queue buildup.
		}

		isOk := taskErr == nil
		errMsg := ""
		if taskErr != nil {
			errMsg = taskErr.Error()
			a.log.Warn("edgeagent: task handling failed",
				zap.String("type", task.Type),
				zap.Error(taskErr),
			)
		}

		if _, reportErr := taskClient.ReportNodeTaskDone(ctx, &pb.ReportNodeTaskDoneRequest{
			NodeTaskId: task.Id,
			IsOk:       isOk,
			Error:      errMsg,
		}); reportErr != nil {
			a.log.Warn("edgeagent: ReportNodeTaskDone failed", zap.Error(reportErr))
		}
	}
}

// syncDomains pulls all NSDomains added/changed/deleted after *version and
// applies them to the ZoneStore.
func (a *Agent) syncDomains(ctx context.Context, client pb.NSDomainServiceClient, version *int64) error {
	const pageSize = 200
	for {
		resp, err := client.ListNSDomainsAfterVersion(ctx, &pb.ListNSDomainsAfterVersionRequest{
			Version: *version,
			Size:    pageSize,
		})
		if err != nil {
			return fmt.Errorf("ListNSDomainsAfterVersion: %w", err)
		}
		if len(resp.NsDomains) == 0 {
			break
		}
		for _, d := range resp.NsDomains {
			apex := iface.FQDN(d.Name)
			if d.IsDeleted || !d.IsOn {
				_ = a.store.Delete(apex)
			} else {
				// Ensure zone exists; records are synced separately.
				snap := a.store.Snapshot()
				if _, ok := snap[apex]; !ok {
					_ = a.store.Update(&iface.Zone{
						Name:    apex,
						Records: make(map[iface.RecordKey][]*iface.Record),
						SOA:     a.buildSOA(apex),
						NS:      a.buildNS(apex),
					})
				}
			}
			if d.Version > *version {
				*version = d.Version
			}
		}
		if int32(len(resp.NsDomains)) < pageSize {
			break
		}
	}
	return nil
}

// syncRecords pulls all NSRecords added/changed/deleted after *version and
// applies them to the ZoneStore.
func (a *Agent) syncRecords(ctx context.Context, client pb.NSRecordServiceClient, version *int64) error {
	const pageSize = 500
	for {
		resp, err := client.ListNSRecordsAfterVersion(ctx, &pb.ListNSRecordsAfterVersionRequest{
			Version: *version,
			Size:    pageSize,
		})
		if err != nil {
			return fmt.Errorf("ListNSRecordsAfterVersion: %w", err)
		}
		if len(resp.NsRecords) == 0 {
			break
		}
		for _, r := range resp.NsRecords {
			if err := a.applyRecord(r); err != nil {
				a.log.Warn("edgeagent: applyRecord failed", zap.Int64("id", r.Id), zap.Error(err))
			}
			if r.Version > *version {
				*version = r.Version
			}
		}
		if int32(len(resp.NsRecords)) < pageSize {
			break
		}
	}
	return nil
}

// applyRecord adds, updates, or removes a single NSRecord in ZoneStore.
// The domain apex is looked up from the store by matching FQDN suffix.
func (a *Agent) applyRecord(r *pb.NSRecord) error {
	if r.Id == 0 {
		return nil
	}

	// Find the zone apex for this record's name.
	// r.Name is a short label (e.g. "www") — we need to find the zone it
	// belongs to. Since we don't have the domain name from the record alone,
	// we look at every zone whose apex is a suffix of the full FQDN candidates.
	snap := a.store.Snapshot()

	// Deleted record: remove by ID across all zones.
	if r.IsDeleted {
		for apex := range snap {
			_ = a.store.DropRecord(apex, r.Id)
		}
		return nil
	}

	// For active records: build the full FQDN for this record.
	// r.Name is a relative label: "@" = zone apex, "www" = subdomain, "*" = wildcard.
	// NsDomain.Name carries the zone name (populated by edgeapi's convertRecordToPB).
	// Without the zone name we fall back to the old behavior of matching via
	// store snapshot, which only works for already-FQDN names.
	var recFQDN string
	if r.NsDomain != nil && r.NsDomain.Name != "" {
		zoneFQDN := iface.FQDN(r.NsDomain.Name)
		if r.Name == "@" {
			recFQDN = zoneFQDN
		} else {
			recFQDN = iface.FQDN(r.Name + "." + r.NsDomain.Name)
		}
	} else {
		recFQDN = iface.FQDN(r.Name)
	}
	var targetApex string
	for apex := range snap {
		if strings.HasSuffix(recFQDN, "."+apex) || recFQDN == apex {
			// Pick the longest (most specific) matching zone.
			if len(apex) > len(targetApex) {
				targetApex = apex
			}
		}
	}

	if targetApex == "" {
		// The zone hasn't been synced yet — skip; it will arrive with domain sync.
		return nil
	}

	qtype, ok := mdns.StringToType[strings.ToUpper(r.Type)]
	if !ok {
		return fmt.Errorf("unknown record type %q", r.Type)
	}

	rrStr := fmt.Sprintf("%s %d IN %s %s", recFQDN, r.Ttl, strings.ToUpper(r.Type), r.Value)
	rr, err := mdns.NewRR(rrStr)
	if err != nil {
		return fmt.Errorf("parse RR %q: %w", rrStr, err)
	}

	if !r.IsOn {
		_ = a.store.DropRecord(targetApex, r.Id)
		return nil
	}

	var routeCodes []string
	var routePriority int32
	for _, route := range r.NsRoutes {
		if route.Code != "" {
			routeCodes = append(routeCodes, route.Code)
			if route.Priority > routePriority {
				routePriority = route.Priority
			}
		}
	}

	rec := &iface.Record{
		ID:            r.Id,
		Name:          recFQDN,
		Type:          qtype,
		TTL:           uint32(r.Ttl),
		Value:         r.Value,
		RR:            rr,
		RouteTags:     nsroute.CodesToTags(routeCodes),
		RoutePriority: routePriority,
	}
	return a.store.PutRecord(targetApex, rec)
}

// rpcCallTimeout bounds every unary RPC agent makes to edgeapi. Run()'s
// initial full sync and poll()'s per-tick calls all share the same
// long-lived, never-cancelled ctx (only cancelled on process shutdown) — a
// single request that hangs (server-side stall, half-open TCP, etc.) would
// otherwise wedge the goroutine forever: no more polling, no more syncing,
// and no log output to even signal it happened, since the hang occurs
// mid-call. Applied here in the interceptor so every call site is covered
// without having to remember to wrap each one individually.
const rpcCallTimeout = 15 * time.Second

// authInterceptor returns a gRPC UnaryClientInterceptor that attaches
// nodeid + token metadata to every outgoing RPC call, and bounds it with
// rpcCallTimeout so a hung call can't wedge the agent forever.
//
// Protocol (matches edgeapi ValidateRequest):
//   - metadata "nodeid" = a.uniqueID
//   - metadata "token"  = base64(AES-256-CFB-encrypt({"type":"dns"}, key=secret, iv=uniqueID))
func (a *Agent) authInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		token, err := a.buildToken()
		if err != nil {
			return fmt.Errorf("edgeagent: build auth token: %w", err)
		}
		ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
		defer cancel()
		md := metadata.Pairs("nodeid", a.uniqueID, "token", token)
		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// buildToken produces the base64-encoded AES-256-CFB encrypted token
// expected by edgeapi's ValidateRequest.
func (a *Agent) buildToken() (string, error) {
	payload, err := json.Marshal(map[string]string{"type": "dns"})
	if err != nil {
		return "", err
	}

	// key = secret, padded/truncated to 32 bytes
	key := []byte(a.secret)
	if len(key) > 32 {
		key = key[:32]
	} else if len(key) < 32 {
		key = append(key, bytes.Repeat([]byte{' '}, 32-len(key))...)
	}

	// iv = uniqueID, padded/truncated to aes.BlockSize (16 bytes)
	iv := []byte(a.uniqueID)
	if len(iv) > aes.BlockSize {
		iv = iv[:aes.BlockSize]
	} else if len(iv) < aes.BlockSize {
		iv = append(iv, bytes.Repeat([]byte{' '}, aes.BlockSize-len(iv))...)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	dst := make([]byte, len(payload))
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(dst, payload)

	return base64.StdEncoding.EncodeToString(dst), nil
}

// errNotConnected is returned by the IP2RegionSource methods below when Run
// hasn't finished its initial dial yet; callers (internal/geo's API updater)
// should treat it like any other transient network error and retry later.
var errNotConnected = errors.New("edgeagent: not connected to edgeapi yet")

// FindPublicArtifact implements geo.APISource, letting dns-edge pull its
// ip2region xdb from edgeapi instead of downloading it from GitHub directly —
// the same authenticated connection/clients Run() already maintains are
// reused here, so no separate credentials are needed.
func (a *Agent) FindPublicArtifact() (v4FileId int64, v6FileId int64, code string, err error) {
	a.clientMu.RLock()
	client := a.ip2RegionArtifactClient
	a.clientMu.RUnlock()
	if client == nil {
		return 0, 0, "", errNotConnected
	}

	resp, err := client.FindPublicIPLibraryArtifact(context.Background(), &pb.FindPublicIPLibraryArtifactRequest{Format: "ip2region"})
	if err != nil {
		return 0, 0, "", err
	}
	var artifact = resp.IpLibraryArtifact
	if artifact == nil {
		return 0, 0, "", nil
	}
	return artifact.FileId, artifact.V6FileId, artifact.Code, nil
}

// DownloadFile implements geo.APISource.
func (a *Agent) DownloadFile(fileId int64, w io.Writer) error {
	if fileId <= 0 {
		return errors.New("invalid fileId")
	}
	a.clientMu.RLock()
	client := a.fileChunkClient
	a.clientMu.RUnlock()
	if client == nil {
		return errNotConnected
	}

	chunkIdsResp, err := client.FindAllFileChunkIds(context.Background(), &pb.FindAllFileChunkIdsRequest{FileId: fileId})
	if err != nil {
		return err
	}
	for _, chunkId := range chunkIdsResp.FileChunkIds {
		chunkResp, err := client.DownloadFileChunk(context.Background(), &pb.DownloadFileChunkRequest{FileChunkId: chunkId})
		if err != nil {
			return err
		}
		if chunkResp.FileChunk == nil {
			return fmt.Errorf("can not find file chunk with chunk id '%d'", chunkId)
		}
		if _, err := w.Write(chunkResp.FileChunk.Data); err != nil {
			return err
		}
	}
	return nil
}
