// White-box tests for the syncer package (same package access for tokenBucket).
package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/testutil"
)

// ── tokenBucket ───────────────────────────────────────────────────────────────

func TestTokenBucket_AllowsUpToCapacity(t *testing.T) {
	b := newTokenBucket(3)
	assert.True(t, b.allow(), "1st token")
	assert.True(t, b.allow(), "2nd token")
	assert.True(t, b.allow(), "3rd token")
	assert.False(t, b.allow(), "bucket exhausted")
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	b := newTokenBucket(1)
	require.True(t, b.allow())
	require.False(t, b.allow(), "should be empty")

	// Wind the clock back to simulate one second passing.
	b.mu.Lock()
	b.last = b.last.Add(-time.Second)
	b.mu.Unlock()

	assert.True(t, b.allow(), "should have refilled after 1s")
}

func TestTokenBucket_CapAtCapacity(t *testing.T) {
	b := newTokenBucket(2)
	// Drain one token
	require.True(t, b.allow())

	// Simulate a very long time passing — tokens must not exceed capacity.
	b.mu.Lock()
	b.last = b.last.Add(-10 * time.Second)
	b.mu.Unlock()

	// Consume all tokens; should be exactly capacity (2), not 11+.
	count := 0
	for b.allow() {
		count++
	}
	assert.Equal(t, 2, count, "tokens should cap at capacity")
}

// ── TriggerSync ───────────────────────────────────────────────────────────────

func TestTriggerSync_NonBlocking(t *testing.T) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	s := New(loader, store, time.Now(), config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100,
	}, zap.NewNop())

	// Three rapid triggers — only one should be queued (buffered(1)).
	require.NoError(t, s.TriggerSync())
	require.NoError(t, s.TriggerSync())
	require.NoError(t, s.TriggerSync())

	assert.Len(t, s.trigger, 1, "channel should hold at most one pending trigger")
}

// ── doSync ────────────────────────────────────────────────────────────────────

func TestDoSync_CallsLoader(t *testing.T) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	since := time.Now().Add(-time.Minute)

	s := New(loader, store, since, config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100,
	}, zap.NewNop())

	s.doSync(context.Background())

	assert.Equal(t, 1, loader.CallCount)
}

func TestDoSync_UpdatesLastAt(t *testing.T) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	before := time.Now().Add(-time.Minute)

	s := New(loader, store, before, config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100,
	}, zap.NewNop())

	s.doSync(context.Background())

	s.mu.Lock()
	lastAt := s.lastAt
	s.mu.Unlock()

	assert.True(t, lastAt.After(before), "lastAt should advance after a successful sync")
}

func TestDoSync_PassesSinceToLoader(t *testing.T) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s := New(loader, store, since, config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 100,
	}, zap.NewNop())

	s.doSync(context.Background())

	assert.Equal(t, since, loader.LastSince)
}

func TestDoSync_RateLimited(t *testing.T) {
	loader := &testutil.MockIncrementalLoader{}
	store := &testutil.MockZoneStore{}

	s := New(loader, store, time.Now(), config.SyncConfig{
		Interval:  time.Hour,
		RateLimit: 1, // capacity = 1
	}, zap.NewNop())

	s.doSync(context.Background()) // consumes the one token
	s.doSync(context.Background()) // should be rate-limited

	assert.Equal(t, 1, loader.CallCount, "second sync should be blocked by token bucket")
}
