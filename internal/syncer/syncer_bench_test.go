// White-box benchmarks for syncer internals.
package syncer

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/testutil"
)

// ── tokenBucket ───────────────────────────────────────────────────────────────

func BenchmarkTokenBucket_Allow(b *testing.B) {
	bucket := newTokenBucket(100_000) // large capacity → always allowed
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = bucket.allow()
	}
}

func BenchmarkTokenBucket_Allow_Parallel(b *testing.B) {
	bucket := newTokenBucket(100_000)
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = bucket.allow()
		}
	})
}

// ── TriggerSync (channel send, non-blocking) ──────────────────────────────────

func BenchmarkTriggerSync(b *testing.B) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	s := New(loader, store, time.Now(), config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100_000,
	}, zap.NewNop())

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.TriggerSync()
		// drain channel so next iteration can queue again
		select {
		case <-s.trigger:
		default:
		}
	}
}

// ── doSync end-to-end (token check + mock IncrementalLoad) ───────────────────

func BenchmarkDoSync(b *testing.B) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	s := New(loader, store, time.Now(), config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100_000,
	}, zap.NewNop())
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		s.doSync(ctx)
	}
}
