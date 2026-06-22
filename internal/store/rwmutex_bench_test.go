package store

// Benchmarks for RWMutexStore read/write paths and contention scenarios.

import (
	"context"
	"fmt"
	"testing"

	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
)

// ── setup helpers ─────────────────────────────────────────────────────────────

// benchStore returns a store pre-loaded with n A records under www.example.com.
func benchStore(b *testing.B, n int) *RWMutexStore {
	b.Helper()
	s := New()
	for i := 0; i < n; i++ {
		rr, _ := mdns.NewRR(fmt.Sprintf("www.example.com. 300 IN A 1.2.3.%d", i%256))
		_ = s.PutRecord("example.com.", &iface.Record{
			ID: int64(i + 1), Name: "www.example.com.", Type: mdns.TypeA, Value: fmt.Sprintf("1.2.3.%d", i%256), RR: rr,
		})
	}
	return s
}

// ── Lookup (pure read) ────────────────────────────────────────────────────────

func BenchmarkLookup(b *testing.B) {
	s := benchStore(b, 1)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Lookup("www.example.com.", mdns.TypeA)
	}
}

func BenchmarkLookup_10Records(b *testing.B) {
	s := benchStore(b, 10)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Lookup("www.example.com.", mdns.TypeA)
	}
}

// ── Lookup parallel (shared read lock contention) ─────────────────────────────

func BenchmarkLookup_Parallel(b *testing.B) {
	s := benchStore(b, 1)
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = s.Lookup("www.example.com.", mdns.TypeA)
		}
	})
}

// ── NameExists ────────────────────────────────────────────────────────────────

func BenchmarkNameExists(b *testing.B) {
	s := benchStore(b, 1)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.NameExists("www.example.com.")
	}
}

func BenchmarkNameExists_Parallel(b *testing.B) {
	s := benchStore(b, 1)
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = s.NameExists("www.example.com.")
		}
	})
}

// ── FindZone ──────────────────────────────────────────────────────────────────

func BenchmarkFindZone_Subdomain(b *testing.B) {
	s := New()
	_ = s.Update(&iface.Zone{Name: "example.com.", Records: map[iface.RecordKey][]*iface.Record{}})
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.FindZone("api.v2.example.com.")
	}
}

// ── PutRecord COW (write path) ────────────────────────────────────────────────

func BenchmarkPutRecord_COW_Append(b *testing.B) {
	s := New()
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		rr, _ := mdns.NewRR("bench.example.com. 300 IN A 1.2.3.4")
		_ = s.PutRecord("example.com.", &iface.Record{
			Name: "bench.example.com.", Type: mdns.TypeA, Value: "1.2.3.4", RR: rr,
		})
		_ = i
	}
}

func BenchmarkPutRecord_COW_Replace(b *testing.B) {
	s := New()
	// Seed one record that will be replaced on each iteration.
	rr0, _ := mdns.NewRR("bench.example.com. 300 IN A 1.2.3.4")
	_ = s.PutRecord("example.com.", &iface.Record{
		ID: 1, Name: "bench.example.com.", Type: mdns.TypeA, Value: "1.2.3.4", RR: rr0,
	})
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		rr, _ := mdns.NewRR("bench.example.com. 300 IN A 9.9.9.9")
		_ = s.PutRecord("example.com.", &iface.Record{
			ID: 1, Name: "bench.example.com.", Type: mdns.TypeA, Value: "9.9.9.9", RR: rr,
		})
	}
}

// ── DropRecord COW ────────────────────────────────────────────────────────────

func BenchmarkDropRecord_COW(b *testing.B) {
	// Measure a drop-and-re-add cycle to avoid depleting the rrset.
	s := New()
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		id := int64(i + 1)
		rr, _ := mdns.NewRR("x.example.com. 300 IN A 1.2.3.4")
		_ = s.PutRecord("example.com.", &iface.Record{
			ID: id, Name: "x.example.com.", Type: mdns.TypeA, Value: "1.2.3.4", RR: rr,
		})
		_ = s.DropRecord("example.com.", id)
	}
}

// ── Read/write contention: parallel readers + one writer ─────────────────────

func BenchmarkLookup_UnderWriteLoad(b *testing.B) {
	s := benchStore(b, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background writer: continuously COW-writes to the same apex.
	go func() {
		i := int64(9000)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				rr, _ := mdns.NewRR("hot.example.com. 300 IN A 1.2.3.4")
				_ = s.PutRecord("example.com.", &iface.Record{
					ID: i, Name: "hot.example.com.", Type: mdns.TypeA, Value: "1.2.3.4", RR: rr,
				})
				i++
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = s.Lookup("www.example.com.", mdns.TypeA)
		}
	})
}
