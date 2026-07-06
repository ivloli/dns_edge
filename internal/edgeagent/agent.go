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
	"fmt"
	"strings"
	"time"

	"github.com/TeaOSLab/EdgeCommon/pkg/rpc/pb"
	mdns "github.com/miekg/dns"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"dns-edge/internal/iface"
)

// Agent polls edgeapi for NS node tasks and refreshes ZoneStore.
type Agent struct {
	store    iface.ZoneStore
	log      *zap.Logger
	uniqueID string
	secret   string
	endpoint string // host:port of edgeapi gRPC
}

// New creates an Agent. endpoint is the edgeapi gRPC address (e.g. "127.0.0.1:8031").
// uniqueID and secret come from the NSNode row in edgeapi's DB.
func New(endpoint, uniqueID, secret string, store iface.ZoneStore, log *zap.Logger) *Agent {
	return &Agent{
		store:    store,
		log:      log,
		uniqueID: uniqueID,
		secret:   secret,
		endpoint: endpoint,
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

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.reportStatusOnShutdown(nsNodeClient)
			return
		case <-ticker.C:
			a.reportStatus(ctx, nsNodeClient, true)
			a.poll(ctx, taskClient, domainClient, recordClient, &domainVersion, &recordVersion)
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

func (a *Agent) poll(
	ctx context.Context,
	taskClient pb.NodeTaskServiceClient,
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
			// Node config changed — nothing to reload in ZoneStore itself; just ack.
			taskErr = nil
		case "nsDomainChanged":
			taskErr = a.syncDomains(ctx, domainClient, domainVersion)
		case "nsRecordChanged":
			taskErr = a.syncRecords(ctx, recordClient, recordVersion)
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

	rec := &iface.Record{
		ID:    r.Id,
		Name:  recFQDN,
		Type:  qtype,
		TTL:   uint32(r.Ttl),
		Value: r.Value,
		RR:    rr,
	}
	return a.store.PutRecord(targetApex, rec)
}

// authInterceptor returns a gRPC UnaryClientInterceptor that attaches
// nodeid + token metadata to every outgoing RPC call.
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
