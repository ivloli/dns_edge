package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/api"
	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
	"dns-edge/internal/testutil"
)

// ── mock RecordStore ──────────────────────────────────────────────────────────

type mockRS struct {
	createZoneFn     func(ctx context.Context, name string) (iface.ZoneMeta, bool, error)
	getZoneFn        func(ctx context.Context, apex string) (iface.ZoneMeta, error)
	softDeleteZoneFn func(ctx context.Context, apex string) error
	listZonesFn      func(ctx context.Context) ([]iface.ZoneMeta, error)
	createRecordFn   func(ctx context.Context, zoneID int64, rec *iface.Record) (*iface.Record, bool, error)
	updateRecordFn   func(ctx context.Context, zoneID, id int64, rec *iface.Record) (*iface.Record, error)
	softDeleteRecordFn func(ctx context.Context, zoneID, id int64) error
	listRecordsFn    func(ctx context.Context, apex string) ([]*iface.Record, error)
}

var _ iface.RecordStore = (*mockRS)(nil)

func (m *mockRS) CreateZone(ctx context.Context, name string) (iface.ZoneMeta, bool, error) {
	if m.createZoneFn != nil {
		return m.createZoneFn(ctx, name)
	}
	return iface.ZoneMeta{}, true, nil
}
func (m *mockRS) GetZone(ctx context.Context, apex string) (iface.ZoneMeta, error) {
	if m.getZoneFn != nil {
		return m.getZoneFn(ctx, apex)
	}
	return iface.ZoneMeta{ID: 1, Name: apex}, nil
}
func (m *mockRS) SoftDeleteZone(ctx context.Context, apex string) error {
	if m.softDeleteZoneFn != nil {
		return m.softDeleteZoneFn(ctx, apex)
	}
	return nil
}
func (m *mockRS) ListZones(ctx context.Context) ([]iface.ZoneMeta, error) {
	if m.listZonesFn != nil {
		return m.listZonesFn(ctx)
	}
	return nil, nil
}
func (m *mockRS) CreateRecord(ctx context.Context, zoneID int64, rec *iface.Record) (*iface.Record, bool, error) {
	if m.createRecordFn != nil {
		return m.createRecordFn(ctx, zoneID, rec)
	}
	return rec, true, nil
}
func (m *mockRS) UpdateRecord(ctx context.Context, zoneID, id int64, rec *iface.Record) (*iface.Record, error) {
	if m.updateRecordFn != nil {
		return m.updateRecordFn(ctx, zoneID, id, rec)
	}
	return rec, nil
}
func (m *mockRS) SoftDeleteRecord(ctx context.Context, zoneID, id int64) error {
	if m.softDeleteRecordFn != nil {
		return m.softDeleteRecordFn(ctx, zoneID, id)
	}
	return nil
}
func (m *mockRS) ListRecords(ctx context.Context, apex string) ([]*iface.Record, error) {
	if m.listRecordsFn != nil {
		return m.listRecordsFn(ctx, apex)
	}
	return nil, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestServer(rs iface.RecordStore, zs iface.ZoneStore) *api.Server {
	return api.New(config.APIConfig{Listen: ":0"}, rs, zs, zap.NewNop())
}

func doRequest(t *testing.T, srv *api.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// ── zone endpoints ────────────────────────────────────────────────────────────

func TestListDomains_Empty(t *testing.T) {
	rs := &mockRS{}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodGet, "/api/v1/domains", nil)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp []any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Empty(t, resp)
}

func TestCreateDomain_Success(t *testing.T) {
	rs := &mockRS{
		createZoneFn: func(_ context.Context, name string) (iface.ZoneMeta, bool, error) {
			return iface.ZoneMeta{ID: 1, Name: name}, true, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/api/v1/domains", map[string]string{"name": "example.com"})

	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "example.com.", resp["name"])
}

func TestCreateDomain_Conflict(t *testing.T) {
	rs := &mockRS{
		createZoneFn: func(_ context.Context, name string) (iface.ZoneMeta, bool, error) {
			return iface.ZoneMeta{ID: 1, Name: name}, false, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/api/v1/domains", map[string]string{"name": "example.com"})

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDeleteDomain_Success(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodDelete, "/api/v1/domains/example.com", nil)

	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestDeleteDomain_NotFound(t *testing.T) {
	rs := &mockRS{
		softDeleteZoneFn: func(_ context.Context, _ string) error {
			return pg.ErrNotFound
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodDelete, "/api/v1/domains/ghost.com", nil)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── record endpoints ──────────────────────────────────────────────────────────

func TestListRecords_Empty(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodGet, "/api/v1/domains/example.com/records", nil)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCreateRecord_Success(t *testing.T) {
	rs := &mockRS{
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			rec.ID = 99
			return rec, true, nil
		},
	}
	body := map[string]any{
		"name":  "www.example.com",
		"type":  "A",
		"ttl":   uint32(300),
		"value": "1.2.3.4",
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/api/v1/domains/example.com/records", body)

	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(99), resp["id"])
}

func TestCreateRecord_InvalidType(t *testing.T) {
	body := map[string]any{
		"name":  "www.example.com",
		"type":  "BADTYPE",
		"ttl":   300,
		"value": "1.2.3.4",
	}
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/api/v1/domains/example.com/records", body)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateRecord_ZoneNotFound(t *testing.T) {
	rs := &mockRS{
		getZoneFn: func(_ context.Context, _ string) (iface.ZoneMeta, error) {
			return iface.ZoneMeta{}, pg.ErrNotFound
		},
	}
	body := map[string]any{"name": "www", "type": "A", "ttl": 300, "value": "1.1.1.1"}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/api/v1/domains/nozone.com/records", body)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestUpdateRecord_Success(t *testing.T) {
	body := map[string]any{"name": "www.example.com", "type": "A", "ttl": 300, "value": "9.9.9.9"}
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPut, "/api/v1/domains/example.com/records/1", body)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDeleteRecord_Success(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodDelete, "/api/v1/domains/example.com/records/1", nil)

	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestDeleteRecord_NotFound(t *testing.T) {
	rs := &mockRS{
		softDeleteRecordFn: func(_ context.Context, _, _ int64) error {
			return pg.ErrNotFound
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/domains/example.com/records/%d", 99), nil)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── health endpoint ───────────────────────────────────────────────────────────

func TestHealthz(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodGet, "/healthz", nil)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"ok"`)
}
