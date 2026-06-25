package api_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

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

func newTestServerWithSecret(rs iface.RecordStore, zs iface.ZoneStore, secret string) *api.Server {
	return api.New(config.APIConfig{Listen: ":0", GoEdgeSecret: secret}, rs, zs, zap.NewNop())
}

func doGoEdge(t *testing.T, srv *api.Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, srv, http.MethodPost, "/goedge/dns", body)
}

func doGoEdgeWithAuth(t *testing.T, srv *api.Server, secret string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(body))
	req := httptest.NewRequest(http.MethodPost, "/goedge/dns", &buf)
	req.Header.Set("Content-Type", "application/json")
	// compute SHA1(secret@timestamp)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := sha1.New()
	h.Write([]byte(secret + "@" + ts))
	req.Header.Set("Timestamp", ts)
	req.Header.Set("Token", fmt.Sprintf("%x", h.Sum(nil)))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
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

// ── GoEdge provider endpoint ──────────────────────────────────────────────────

func TestGoEdge_UnknownAction(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "UnknownOp"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGoEdge_DefaultRoute(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "DefaultRoute"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "default", rr.Body.String())
}

func TestGoEdge_GetRoutes(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "GetRoutes", "domain": "example.com."})
	assert.Equal(t, http.StatusOK, rr.Code)
	var routes []map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&routes))
	require.Len(t, routes, 1)
	assert.Equal(t, "default", routes[0]["code"])
}

func TestGoEdge_GetDomains(t *testing.T) {
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}, {ID: 2, Name: "foo.com."}}, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "GetDomains"})
	assert.Equal(t, http.StatusOK, rr.Code)
	var domains []string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&domains))
	assert.Equal(t, []string{"example.com.", "foo.com."}, domains)
}

func TestGoEdge_GetRecords(t *testing.T) {
	rs := &mockRS{
		listRecordsFn: func(_ context.Context, apex string) ([]*iface.Record, error) {
			return []*iface.Record{testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0)}, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "GetRecords", "domain": "example.com."})
	assert.Equal(t, http.StatusOK, rr.Code)
	var records []map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&records))
	require.Len(t, records, 1)
	assert.Equal(t, "www.example.com.", records[0]["name"])
	assert.Equal(t, "A", records[0]["type"])
	assert.Equal(t, "default", records[0]["route"]) // empty RouteTags → "default"
}

func TestGoEdge_GetRecords_MissingDomain(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "GetRecords"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGoEdge_QueryRecord_Found(t *testing.T) {
	rs := &mockRS{
		listRecordsFn: func(_ context.Context, apex string) ([]*iface.Record, error) {
			return []*iface.Record{
				testutil.MakeA("www.example.com.", "1.2.3.4", 300, 0),
				testutil.MakeA("mail.example.com.", "5.6.7.8", 300, 0),
			}, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action":     "QueryRecord",
		"domain":     "example.com.",
		"name":       "www.example.com.",
		"recordType": "A",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	var rec map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&rec))
	assert.Equal(t, "www.example.com.", rec["name"])
}

func TestGoEdge_QueryRecord_NotFound(t *testing.T) {
	rs := &mockRS{
		listRecordsFn: func(_ context.Context, apex string) ([]*iface.Record, error) {
			return nil, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action":     "QueryRecord",
		"domain":     "example.com.",
		"name":       "ghost.example.com.",
		"recordType": "A",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "null", rr.Body.String())
}

func TestGoEdge_AddRecord_Success(t *testing.T) {
	var capturedRec *iface.Record
	rs := &mockRS{
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			rec.ID = 42
			capturedRec = rec
			return rec, true, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "AddRecord",
		"domain": "example.com.",
		"newRecord": map[string]any{
			"name":  "cdn.example.com.",
			"type":  "A",
			"value": "10.0.0.1",
			"ttl":   300,
			"route": "default",
		},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedRec)
	assert.Equal(t, "cdn.example.com.", capturedRec.Name)
	assert.Equal(t, "", capturedRec.RouteTags) // "default" route → empty RouteTags
}

func TestGoEdge_AddRecord_WithRouteTags(t *testing.T) {
	var capturedRec *iface.Record
	rs := &mockRS{
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			rec.ID = 43
			capturedRec = rec
			return rec, true, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "AddRecord",
		"domain": "example.com.",
		"newRecord": map[string]any{
			"name":  "cdn.example.com.",
			"type":  "A",
			"value": "10.0.0.2",
			"ttl":   300,
			"route": "country=中国;isp=电信",
		},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedRec)
	assert.Equal(t, "country=中国;isp=电信", capturedRec.RouteTags)
}

func TestGoEdge_AddRecord_ZoneNotFound(t *testing.T) {
	rs := &mockRS{
		getZoneFn: func(_ context.Context, _ string) (iface.ZoneMeta, error) {
			return iface.ZoneMeta{}, pg.ErrNotFound
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "AddRecord",
		"domain": "ghost.com.",
		"newRecord": map[string]any{
			"name":  "cdn.ghost.com.",
			"type":  "A",
			"value": "1.1.1.1",
			"ttl":   300,
			"route": "default",
		},
	})
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGoEdge_AddRecord_MissingNewRecord(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "AddRecord", "domain": "example.com."})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGoEdge_UpdateRecord_Success(t *testing.T) {
	var updatedID int64
	rs := &mockRS{
		updateRecordFn: func(_ context.Context, _ int64, id int64, rec *iface.Record) (*iface.Record, error) {
			updatedID = id
			rec.ID = id
			return rec, nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "UpdateRecord",
		"domain": "example.com.",
		"record": map[string]any{"id": "7", "name": "cdn.example.com.", "type": "A", "value": "1.1.1.1", "ttl": 300, "route": "default"},
		"newRecord": map[string]any{"name": "cdn.example.com.", "type": "A", "value": "2.2.2.2", "ttl": 600, "route": "default"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, int64(7), updatedID)
}

func TestGoEdge_UpdateRecord_InvalidID(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "UpdateRecord",
		"domain": "example.com.",
		"record":    map[string]any{"id": "notanint", "name": "x.", "type": "A", "value": "1.1.1.1", "ttl": 300, "route": "default"},
		"newRecord": map[string]any{"name": "x.", "type": "A", "value": "2.2.2.2", "ttl": 300, "route": "default"},
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGoEdge_DeleteRecord_Success(t *testing.T) {
	var deletedID int64
	rs := &mockRS{
		softDeleteRecordFn: func(_ context.Context, _, id int64) error {
			deletedID = id
			return nil
		},
	}
	srv := newTestServer(rs, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{
		"action": "DeleteRecord",
		"domain": "example.com.",
		"record": map[string]any{"id": "55", "name": "cdn.example.com.", "type": "A", "value": "1.1.1.1", "ttl": 300, "route": "default"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, int64(55), deletedID)
}

func TestGoEdge_DeleteRecord_MissingRecord(t *testing.T) {
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "DeleteRecord", "domain": "example.com."})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── GoEdge auth ───────────────────────────────────────────────────────────────

func TestGoEdge_Auth_ValidToken(t *testing.T) {
	const secret = "s3cr3t"
	srv := newTestServerWithSecret(&mockRS{}, &testutil.MockZoneStore{}, secret)
	rr := doGoEdgeWithAuth(t, srv, secret, map[string]any{"action": "DefaultRoute"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "default", rr.Body.String())
}

func TestGoEdge_Auth_MissingHeaders(t *testing.T) {
	const secret = "s3cr3t"
	srv := newTestServerWithSecret(&mockRS{}, &testutil.MockZoneStore{}, secret)
	// doGoEdge sends no auth headers
	rr := doGoEdge(t, srv, map[string]any{"action": "DefaultRoute"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestGoEdge_Auth_WrongSecret(t *testing.T) {
	srv := newTestServerWithSecret(&mockRS{}, &testutil.MockZoneStore{}, "correct")
	rr := doGoEdgeWithAuth(t, srv, "wrong", map[string]any{"action": "DefaultRoute"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestGoEdge_Auth_Disabled_NoHeadersNeeded(t *testing.T) {
	// goedge_secret = "" → auth disabled
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doGoEdge(t, srv, map[string]any{"action": "DefaultRoute"})
	assert.Equal(t, http.StatusOK, rr.Code)
}
