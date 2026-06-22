// Package syncer implements incremental DNS record synchronisation from PostgreSQL.
//
// Sync is triggered two ways:
//   - Timer: fires every Config.Interval (default 30 s).
//   - Probabilistic: the DNS handler calls TriggerSync on ~Config.Prob fraction of queries.
//
// Both paths go through a Token Bucket (capacity = Config.RateLimit) to prevent
// multiple instances from thundering into PG simultaneously.
package syncer

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/iface"
	"dns-edge/internal/metrics"
)

// PGSyncer pulls incremental record changes from PostgreSQL into ZoneStore.
type PGSyncer struct {
	loader   iface.IncrementalLoader
	store    iface.ZoneStore
	log      *zap.Logger
	interval time.Duration

	mu     sync.Mutex
	lastAt time.Time // timestamp of last successful sync (protected by mu)

	bucket  *tokenBucket
	trigger chan struct{} // buffered(1); probabilistic callers send here
}

// New returns a PGSyncer ready to Start. since is the timestamp used for the
// very first incremental pull — set it to just before LoadAll began so that
// any records written during startup are not missed.
func New(loader iface.IncrementalLoader, store iface.ZoneStore, since time.Time,
	cfg config.SyncConfig, log *zap.Logger) *PGSyncer {

	capacity := cfg.RateLimit
	if capacity <= 0 {
		capacity = 1
	}
	return &PGSyncer{
		loader:   loader,
		store:    store,
		log:      log,
		interval: cfg.Interval,
		lastAt:   since,
		bucket:   newTokenBucket(capacity),
		trigger:  make(chan struct{}, 1),
	}
}

// TriggerSync requests an out-of-band sync.  Non-blocking: if a trigger is
// already pending the call is a no-op.  Rate limiting is applied inside doSync.
func (s *PGSyncer) TriggerSync() error {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
	return nil
}

// Start blocks until ctx is cancelled, running the sync loop.
// Call from a dedicated goroutine.
func (s *PGSyncer) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.doSync(ctx)
		case <-s.trigger:
			s.doSync(ctx)
		}
	}
}

// doSync performs one incremental load if the token bucket permits.
func (s *PGSyncer) doSync(ctx context.Context) {
	if !s.bucket.allow() {
		s.log.Debug("sync skipped: rate-limited")
		return
	}

	s.mu.Lock()
	since := s.lastAt
	s.mu.Unlock()

	// Record the start time before querying so that changes written during
	// the query window are caught by the next sync.
	queryStart := time.Now()

	if err := s.loader.IncrementalLoad(ctx, since, s.store); err != nil {
		s.log.Error("incremental sync failed", zap.Error(err))
		metrics.SyncTotal.WithLabelValues("error").Inc()
		return
	}

	elapsed := time.Since(queryStart)
	metrics.SyncTotal.WithLabelValues("success").Inc()
	metrics.SyncDuration.Observe(elapsed.Seconds())
	metrics.SyncLastSuccess.SetToCurrentTime()

	s.mu.Lock()
	s.lastAt = queryStart
	s.mu.Unlock()
}

// ── token bucket ──────────────────────────────────────────────────────────────

type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens per nanosecond
	last     time.Time
}

func newTokenBucket(capacity int) *tokenBucket {
	return &tokenBucket{
		tokens:   float64(capacity),
		capacity: float64(capacity),
		// refill: capacity tokens per second
		rate: float64(capacity) / float64(time.Second),
		last: time.Now(),
	}
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := float64(now.Sub(b.last))
	b.last = now
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
