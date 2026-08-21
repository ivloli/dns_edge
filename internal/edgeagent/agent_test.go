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
// is nil, so any other call panics loudly rather than quietly returning a zero
// value. That matters here: the first version of this fix called
// ListNSRecords, which a DNS node isn't allowed to call at all, and a fake
// that answered every method would have hidden it.
type fakeDomainClient struct {
	pb.NSDomainServiceClient
	domains []*pb.NSDomain
	calls   int
}

func (f *fakeDomainClient) ListNSDomainsAfterVersion(ctx context.Context, in *pb.ListNSDomainsAfterVersionRequest, opts ...grpc.CallOption) (*pb.ListNSDomainsAfterVersionResponse, error) {
	f.calls++
	if f.calls > 1 {
		// An empty page is how syncDomains knows to stop.
		return &pb.ListNSDomainsAfterVersionResponse{}, nil
	}
	return &pb.ListNSDomainsAfterVersionResponse{NsDomains: f.domains}, nil
}

// fakeRecordClient answers ListNSRecordsAfterVersion — the only record query a
// DNS node is authorised to make — and records the cursors it was asked for so
// a test can tell a rewind from an ordinary incremental poll.
type fakeRecordClient struct {
	pb.NSRecordServiceClient
	records         []*pb.NSRecord
	requestedFromV0 int
	calls           int
}

func (f *fakeRecordClient) ListNSRecordsAfterVersion(ctx context.Context, in *pb.ListNSRecordsAfterVersionRequest, opts ...grpc.CallOption) (*pb.ListNSRecordsAfterVersionResponse, error) {
	f.calls++
	if in.Version == 0 {
		f.requestedFromV0++
		return &pb.ListNSRecordsAfterVersionResponse{NsRecords: f.records}, nil
	}
	// Past the cursor there is nothing new — which is exactly why a zone
	// recreated after the cursor moved on would otherwise stay empty.
	return &pb.ListNSRecordsAfterVersionResponse{}, nil
}

func newTestAgent(zoneStore iface.ZoneStore) *Agent {
	return &Agent{store: zoneStore, log: zap.NewNop()}
}

func testRecords() []*pb.NSRecord {
	return []*pb.NSRecord{
		{Id: 1, Name: "www", Type: "A", Value: "1.2.3.4", Ttl: 600, IsOn: true, Version: 50,
			NsDomain: &pb.NSDomain{Id: 7, Name: "example.com"}},
	}
}

// A zone created during a domain sync must come back with its records, not
// empty. Record sync is incremental, so records older than the cursor are
// never re-delivered on their own.
func TestSyncDomains_RefillsRecordsForNewZone(t *testing.T) {
	var zoneStore = store.New()
	var agent = newTestAgent(zoneStore)

	var domainClient = &fakeDomainClient{domains: []*pb.NSDomain{
		{Id: 7, Name: "example.com", IsOn: true, Version: 100},
	}}
	var recordClient = &fakeRecordClient{records: testRecords()}

	// A cursor well past the records, as it would be after they were synced
	// once and the zone was later dropped.
	var version int64
	var recordVersion int64 = 9999

	if err := agent.syncDomains(context.Background(), domainClient, recordClient, &version, &recordVersion); err != nil {
		t.Fatalf("syncDomains: %v", err)
	}

	if recordClient.requestedFromV0 != 1 {
		t.Fatalf("expected the record cursor to be rewound once, got %d requests from 0", recordClient.requestedFromV0)
	}
	var records = zoneStore.Lookup("www.example.com.", mdns.TypeA)
	if len(records) != 1 {
		t.Fatalf("expected the zone's record to be present, got %d", len(records))
	}
	if records[0].Value != "1.2.3.4" {
		t.Errorf("unexpected record value %q", records[0].Value)
	}
	if version != 100 {
		t.Errorf("expected domain cursor to advance to 100, got %d", version)
	}
}

// Re-syncing a domain whose zone is already there must not rewind: that path
// runs on every nsDomainChanged task, and rewinding each time would re-pull
// every record the node serves.
func TestSyncDomains_NoRewindWhenZoneAlreadyExists(t *testing.T) {
	var zoneStore = store.New()
	_ = zoneStore.Update(&iface.Zone{
		Name:    "example.com.",
		Records: make(map[iface.RecordKey][]*iface.Record),
	})

	var agent = newTestAgent(zoneStore)
	var recordClient = &fakeRecordClient{records: testRecords()}

	var version int64
	var recordVersion int64 = 9999
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: true, Version: 100}}},
		recordClient, &version, &recordVersion); err != nil {
		t.Fatalf("syncDomains: %v", err)
	}

	if recordClient.calls != 0 {
		t.Errorf("expected no record sync for an existing zone, got %d calls", recordClient.calls)
	}
	if recordVersion != 9999 {
		t.Errorf("expected the record cursor to be left alone, got %d", recordVersion)
	}
}

// Switching a domain off drops its zone; switching it back on must restore the
// records rather than leave an empty zone behind. This is the sequence that
// silently emptied a live zone.
func TestSyncDomains_DisableThenEnableRestoresRecords(t *testing.T) {
	var zoneStore = store.New()
	var agent = newTestAgent(zoneStore)
	var recordClient = &fakeRecordClient{records: testRecords()}

	var version int64
	var recordVersion int64
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: true, Version: 100}}},
		recordClient, &version, &recordVersion); err != nil {
		t.Fatalf("initial syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 1 {
		t.Fatal("record missing after initial sync")
	}

	// The cursor has moved past the records by now, as it would in practice.
	recordVersion = 9999

	version = 0
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: false, Version: 200}}},
		recordClient, &version, &recordVersion); err != nil {
		t.Fatalf("disable syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 0 {
		t.Fatal("zone should be gone while the domain is off")
	}

	version = 0
	if err := agent.syncDomains(context.Background(),
		&fakeDomainClient{domains: []*pb.NSDomain{{Id: 7, Name: "example.com", IsOn: true, Version: 300}}},
		recordClient, &version, &recordVersion); err != nil {
		t.Fatalf("re-enable syncDomains: %v", err)
	}
	if len(zoneStore.Lookup("www.example.com.", mdns.TypeA)) != 1 {
		t.Fatal("records did not come back when the domain was re-enabled")
	}
}
