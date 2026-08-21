package edgeagent

import (
	"context"
	"testing"

	mdns "github.com/miekg/dns"
	"gitlab.gainetics.io/backend-cdn/goedge/edgecommon/pkg/rpc/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"dns-edge/internal/iface"
	"dns-edge/internal/store"
)

// Only the one method each fake needs is implemented; the embedded interface
// is nil, so any other call would panic loudly rather than quietly return a
// zero value.
type fakeDomainClient struct {
	pb.NSDomainServiceClient
	domains []*pb.NSDomain
	calls   int
}

func (f *fakeDomainClient) ListNSDomainsAfterVersion(ctx context.Context, in *pb.ListNSDomainsAfterVersionRequest, opts ...grpc.CallOption) (*pb.ListNSDomainsAfterVersionResponse, error) {
	f.calls++
	if f.calls > 1 {
		// Second page is empty, which is how syncDomains knows to stop.
		return &pb.ListNSDomainsAfterVersionResponse{}, nil
	}
	return &pb.ListNSDomainsAfterVersionResponse{NsDomains: f.domains}, nil
}

type fakeRecordClient struct {
	pb.NSRecordServiceClient
	recordsByDomain map[int64][]*pb.NSRecord
	listedDomains   []int64
}

func (f *fakeRecordClient) ListNSRecords(ctx context.Context, in *pb.ListNSRecordsRequest, opts ...grpc.CallOption) (*pb.ListNSRecordsResponse, error) {
	if in.Offset > 0 {
		return &pb.ListNSRecordsResponse{}, nil
	}
	f.listedDomains = append(f.listedDomains, in.NsDomainId)
	return &pb.ListNSRecordsResponse{NsRecords: f.recordsByDomain[in.NsDomainId]}, nil
}

func newTestAgent(zoneStore iface.ZoneStore) *Agent {
	return &Agent{store: zoneStore, log: zap.NewNop()}
}

// A zone that is created during a domain sync must come back with its records,
// not empty. Record sync is incremental, so records older than the cursor are
// never re-delivered on their own — a domain switched off and on again would
// otherwise resolve NXDOMAIN forever despite its records still existing in
// edgeapi, with nothing reported anywhere.
func TestSyncDomains_BackfillsRecordsForNewZone(t *testing.T) {
	var zoneStore = store.New()
	var agent = newTestAgent(zoneStore)

	var domainClient = &fakeDomainClient{domains: []*pb.NSDomain{
		{Id: 7, Name: "example.com", IsOn: true, Version: 100},
	}}
	var recordClient = &fakeRecordClient{recordsByDomain: map[int64][]*pb.NSRecord{
		7: {
			{Id: 1, Name: "www", Type: "A", Value: "1.2.3.4", Ttl: 600, IsOn: true,
				NsDomain: &pb.NSDomain{Id: 7, Name: "example.com"}},
		},
	}}

	var version int64
	if err := agent.syncDomains(context.Background(), domainClient, recordClient, &version); err != nil {
		t.Fatalf("syncDomains: %v", err)
	}

	if len(recordClient.listedDomains) != 1 || recordClient.listedDomains[0] != 7 {
		t.Fatalf("expected records to be fetched for domain 7, got %v", recordClient.listedDomains)
	}

	var records = zoneStore.Lookup("www.example.com.", mdns.TypeA)
	if len(records) != 1 {
		t.Fatalf("expected the zone's record to be present, got %d", len(records))
	}
	if records[0].Value != "1.2.3.4" {
		t.Errorf("unexpected record value %q", records[0].Value)
	}

	if version != 100 {
		t.Errorf("expected version cursor to advance to 100, got %d", version)
	}
}

// Re-syncing a domain whose zone is already present must not re-fetch its
// records: that path runs on every nsDomainChanged task, and a domain list of
// any size would turn each one into a full record scan.
func TestSyncDomains_SkipsBackfillWhenZoneAlreadyExists(t *testing.T) {
	var zoneStore = store.New()
	_ = zoneStore.Update(&iface.Zone{
		Name:    "example.com.",
		Records: make(map[iface.RecordKey][]*iface.Record),
	})

	var agent = newTestAgent(zoneStore)
	var domainClient = &fakeDomainClient{domains: []*pb.NSDomain{
		{Id: 7, Name: "example.com", IsOn: true, Version: 100},
	}}
	var recordClient = &fakeRecordClient{recordsByDomain: map[int64][]*pb.NSRecord{}}

	var version int64
	if err := agent.syncDomains(context.Background(), domainClient, recordClient, &version); err != nil {
		t.Fatalf("syncDomains: %v", err)
	}

	if len(recordClient.listedDomains) != 0 {
		t.Errorf("expected no record fetch for an existing zone, got %v", recordClient.listedDomains)
	}
}

// A disabled domain drops its zone, and re-enabling it must restore the
// records rather than leave an empty zone behind — this is the sequence that
// silently emptied a live zone.
func TestSyncDomains_DisableThenEnableRestoresRecords(t *testing.T) {
	var zoneStore = store.New()
	var agent = newTestAgent(zoneStore)

	var recordClient = &fakeRecordClient{recordsByDomain: map[int64][]*pb.NSRecord{
		7: {
			{Id: 1, Name: "www", Type: "A", Value: "1.2.3.4", Ttl: 600, IsOn: true,
				NsDomain: &pb.NSDomain{Id: 7, Name: "example.com"}},
		},
	}}

	var version int64
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: true, Version: 100}}},
		recordClient, &version); err != nil {
		t.Fatalf("initial syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 1 {
		t.Fatal("record missing after initial sync")
	}

	// Switched off: the whole zone goes.
	version = 0
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: false, Version: 200}}},
		recordClient, &version); err != nil {
		t.Fatalf("disable syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 0 {
		t.Fatal("zone should be gone while the domain is off")
	}

	// Switched back on: the records must return with it.
	version = 0
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: true, Version: 300}}},
		recordClient, &version); err != nil {
		t.Fatalf("re-enable syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 1 {
		t.Fatal("records did not come back when the domain was re-enabled")
	}
}
