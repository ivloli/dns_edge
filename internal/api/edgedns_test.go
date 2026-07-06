package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/api"
	"dns-edge/internal/iface"
	"dns-edge/internal/store"
	"dns-edge/internal/testutil"
)

// ── helpers ───────────────────────────────────────────────────────────────────
//
// edgeDNSAPI handlers (edgedns_provider.go) read and write iface.ZoneStore
// exclusively — the "no-PG" migration — so fixtures for these tests live in a
// real store.RWMutexStore rather than in mockRS (which only backs the
// separate goedge_provider.go/record.go surface tested by api_test.go).

func newEdgeDNSSrv(rs iface.RecordStore, zs iface.ZoneStore) *api.Server {
	return api.New(config.APIConfig{
		Listen:           ":0",
		EdgeDNSKeyID:     "testkey",
		EdgeDNSKeySecret: "testsecret",
	}, rs, zs, zap.NewNop())
}

// getToken exchanges credentials for a bearer token.
func getToken(t *testing.T, srv *api.Server) string {
	t.Helper()
	rr := doRequest(t, srv, http.MethodPost, "/APIAccessTokenService/getAPIAccessToken", map[string]any{
		"type":        "user",
		"accessKeyId": "testkey",
		"accessKey":   "testsecret",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, float64(200), resp["code"], "token request failed: %s", rr.Body.String())
	data := resp["data"].(map[string]any)
	return data["token"].(string)
}

// authed posts path with the given bearer token.
func authed(t *testing.T, srv *api.Server, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Edge-Access-Token", token)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// edgeDNSCode extracts the "code" field from an edgeDNS JSON response.
func edgeDNSCode(t *testing.T, rr *httptest.ResponseRecorder) float64 {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	return resp["code"].(float64)
}

// findDomainID discovers the nsDomainId a real GoEdge client would get for
// name via FindNSDomainWithName — the id is an internal FNV hash of the
// apex (see zoneID in edgedns_provider.go), so tests resolve it through the
// API instead of hardcoding it. Decoded via json.Number/Int64 rather than
// the default float64, since the FNV hash regularly exceeds 2^53 and a
// float64 round-trip would silently corrupt it before it's sent back in a
// follow-up request.
func findDomainID(t *testing.T, srv *api.Server, token, name string) int64 {
	t.Helper()
	rr := authed(t, srv, token, "/NSDomainService/FindNSDomainWithName", map[string]any{"name": name})
	require.Equal(t, http.StatusOK, rr.Code)
	dec := json.NewDecoder(rr.Body)
	dec.UseNumber()
	var resp map[string]any
	require.NoError(t, dec.Decode(&resp))
	data := resp["data"].(map[string]any)
	domain := data["nsDomain"].(map[string]any)
	id, err := domain["id"].(json.Number).Int64()
	require.NoError(t, err)
	return id
}

// findStoredRecord looks up the single record stored under (name, qtype) in
// apex's zone, for asserting on fields (like RouteTags) the wire response
// doesn't echo back directly.
func findStoredRecord(t *testing.T, zs iface.ZoneStore, apex, name string, qtype uint16) *iface.Record {
	t.Helper()
	zone, ok := zs.Snapshot()[apex]
	require.True(t, ok, "zone %q not found in store", apex)
	recs := zone.Records[iface.RecordKey{Name: name, Qtype: qtype}]
	require.Len(t, recs, 1)
	return recs[0]
}

// ── APIAccessTokenService ─────────────────────────────────────────────────────

func TestEdgeDNS_GetToken_Success(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/APIAccessTokenService/getAPIAccessToken", map[string]any{
		"type":        "user",
		"accessKeyId": "testkey",
		"accessKey":   "testsecret",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	assert.NotEmpty(t, data["token"])
	assert.Greater(t, data["expiresAt"].(float64), float64(0))
}

func TestEdgeDNS_GetToken_WrongCredentials(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/APIAccessTokenService/getAPIAccessToken", map[string]any{
		"type":        "user",
		"accessKeyId": "testkey",
		"accessKey":   "wrongsecret",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(401), edgeDNSCode(t, rr))
}

func TestEdgeDNS_GetToken_NotConfigured(t *testing.T) {
	// no EdgeDNSKeyID set -> handler returns HTTP 404 with JSON body
	srv := newTestServer(&mockRS{}, &testutil.MockZoneStore{})
	rr := doRequest(t, srv, http.MethodPost, "/APIAccessTokenService/getAPIAccessToken", map[string]any{
		"accessKeyId": "x", "accessKey": "y",
	})
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestEdgeDNS_RequiresToken(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	// no token header -> 401
	rr := doRequest(t, srv, http.MethodPost, "/NSDomainService/ListNSDomains", map[string]any{})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestEdgeDNS_TokenReuse(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	// same token works for multiple requests
	for range 3 {
		rr := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{})
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, float64(200), edgeDNSCode(t, rr))
	}
}

// ── NSDomainService ───────────────────────────────────────────────────────────

func TestEdgeDNS_ListDomains(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.")))
	require.NoError(t, zs.Update(testutil.MakeZone("foo.org.")))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{"offset": 0, "size": 10})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	domains := data["nsDomains"].([]any)
	require.Len(t, domains, 2)
	// results are sorted by name for stable pagination: example.com before foo.org
	assert.Equal(t, "example.com", domains[0].(map[string]any)["name"])
}

func TestEdgeDNS_ListDomains_Pagination(t *testing.T) {
	zs := store.New()
	for _, apex := range []string{"a.com.", "b.com.", "c.com."} {
		require.NoError(t, zs.Update(testutil.MakeZone(apex)))
	}
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{"offset": 1, "size": 1})
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	domains := data["nsDomains"].([]any)
	require.Len(t, domains, 1)
	assert.Equal(t, "b.com", domains[0].(map[string]any)["name"])
}

func TestEdgeDNS_FindDomain_Found(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.")))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)

	listRR := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{"offset": 0, "size": 10})
	var listResp map[string]any
	require.NoError(t, json.NewDecoder(listRR.Body).Decode(&listResp))
	wantID := listResp["data"].(map[string]any)["nsDomains"].([]any)[0].(map[string]any)["id"]

	rr := authed(t, srv, token, "/NSDomainService/FindNSDomainWithName", map[string]any{"name": "example.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	domain := data["nsDomain"].(map[string]any)
	// id is a deterministic hash of the apex, so the same zone must resolve
	// to the same id whether reached via ListNSDomains or FindNSDomainWithName.
	assert.Equal(t, wantID, domain["id"])
	assert.Equal(t, "example.com", domain["name"])
}

func TestEdgeDNS_FindDomain_LazyCreatesZone(t *testing.T) {
	zs := store.New() // no zones yet
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)

	rr := authed(t, srv, token, "/NSDomainService/FindNSDomainWithName", map[string]any{"name": "ghost.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	domain := data["nsDomain"].(map[string]any)
	assert.Equal(t, "ghost.com", domain["name"])

	// GoEdge never calls CreateNSDomain, so dns-edge must lazily create the
	// zone on first lookup — otherwise GoEdge could never create records for
	// a domain it just added.
	_, ok := zs.Snapshot()["ghost.com."]
	assert.True(t, ok, "zone should have been lazily created in the store")
}

// ── NSRecordService ───────────────────────────────────────────────────────────

func TestEdgeDNS_ListRecords(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.",
		&iface.Record{ID: 10, Name: "www.example.com.", Type: mdns.TypeA, Value: "1.2.3.4", TTL: 300},
		&iface.Record{ID: 11, Name: "www.example.com.", Type: mdns.TypeA, Value: "5.6.7.8", TTL: 300, RouteTags: "province=上海"},
	)))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/ListNSRecords", map[string]any{"nsDomainId": domainID, "offset": 0, "size": 10})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	records := data["nsRecords"].([]any)
	require.Len(t, records, 2)
	// second record (by id) has the province route — verify route_tags -> nsRoutes conversion
	r1 := records[1].(map[string]any)
	routes := r1["nsRoutes"].([]any)
	require.Len(t, routes, 1)
	assert.Equal(t, "province:上海", routes[0].(map[string]any)["code"])
}

func TestEdgeDNS_ListRecords_MissingDomainID(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/ListNSRecords", map[string]any{"offset": 0, "size": 10})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(400), edgeDNSCode(t, rr))
}

func TestEdgeDNS_CreateRecord_DefaultRoute(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.")))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   domainID,
		"name":         "www",
		"type":         "A",
		"value":        "1.2.3.4",
		"ttl":          300,
		"nsRouteCodes": []string{"default"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(200), edgeDNSCode(t, rr))

	rec := findStoredRecord(t, zs, "example.com.", "www.example.com.", mdns.TypeA)
	assert.Equal(t, "", rec.RouteTags) // "default" -> empty route_tags
}

func TestEdgeDNS_CreateRecord_ProvinceAndISP(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.")))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   domainID,
		"name":         "www",
		"type":         "A",
		"value":        "2.2.2.2",
		"ttl":          300,
		"nsRouteCodes": []string{"province:上海", "isp:电信"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)

	// nsRouteCodes -> route_tags: "province:上海;isp:电信" stored as "province=上海;isp=电信"
	rec := findStoredRecord(t, zs, "example.com.", "www.example.com.", mdns.TypeA)
	assert.Equal(t, "province=上海;isp=电信", rec.RouteTags)
}

func TestEdgeDNS_CreateRecord_CountryRoute(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.")))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   domainID,
		"name":         "cdn",
		"type":         "A",
		"value":        "3.3.3.3",
		"ttl":          300,
		"nsRouteCodes": []string{"country:中国"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)

	rec := findStoredRecord(t, zs, "example.com.", "cdn.example.com.", mdns.TypeA)
	assert.Equal(t, "country=中国", rec.RouteTags)
}

func TestEdgeDNS_CreateRecord_ZoneNotFound(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, store.New()) // no zones -> 999 not found
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   999,
		"name":         "www",
		"type":         "A",
		"value":        "1.1.1.1",
		"ttl":          300,
		"nsRouteCodes": []string{},
	})
	assert.Equal(t, float64(404), edgeDNSCode(t, rr))
}

func TestEdgeDNS_DeleteRecord_Success(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.",
		&iface.Record{ID: 99, Name: "www.example.com.", Type: mdns.TypeA, Value: "1.1.1.1", TTL: 300},
	)))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/DeleteNSRecord", map[string]any{"nsRecordId": 99})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(200), edgeDNSCode(t, rr))

	zone := zs.Snapshot()["example.com."]
	assert.Empty(t, zone.Records[iface.RecordKey{Name: "www.example.com.", Qtype: mdns.TypeA}])
}

func TestEdgeDNS_DeleteRecord_MissingID(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/DeleteNSRecord", map[string]any{})
	assert.Equal(t, float64(400), edgeDNSCode(t, rr))
}

// ── NSRouteService ────────────────────────────────────────────────────────────

func TestEdgeDNS_WorldRegionRoutes(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRouteService/FindAllDefaultWorldRegionRoutes", map[string]any{})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	routes := data["nsRoutes"].([]any)
	assert.NotEmpty(t, routes)
	for _, r := range routes {
		code := r.(map[string]any)["code"].(string)
		assert.Contains(t, code, "country:")
	}
}

func TestEdgeDNS_ChinaProvinceRoutes(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRouteService/FindAllDefaultChinaProvinceRoutes", map[string]any{})
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	routes := data["nsRoutes"].([]any)
	assert.Len(t, routes, 31)
	codes := make(map[string]bool)
	for _, r := range routes {
		codes[r.(map[string]any)["code"].(string)] = true
	}
	assert.True(t, codes["province:上海"])
	assert.True(t, codes["province:北京"])
	assert.True(t, codes["province:广东"])
}

func TestEdgeDNS_ISPRoutes(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRouteService/FindAllDefaultISPRoutes", map[string]any{})
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	routes := data["nsRoutes"].([]any)
	assert.Len(t, routes, 4)
	codes := make(map[string]bool)
	for _, r := range routes {
		codes[r.(map[string]any)["code"].(string)] = true
	}
	assert.True(t, codes["isp:电信"])
	assert.True(t, codes["isp:联通"])
	assert.True(t, codes["isp:移动"])
}

func TestEdgeDNS_AgentAndCustomRoutes_Empty(t *testing.T) {
	srv := newEdgeDNSSrv(&mockRS{}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	for _, path := range []string{
		"/NSRouteService/FindAllAgentNSRoutes",
		"/NSRouteService/FindAllNSRoutes",
	} {
		rr := authed(t, srv, token, path, map[string]any{})
		var resp map[string]any
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		data := resp["data"].(map[string]any)
		routes := data["nsRoutes"].([]any)
		assert.Empty(t, routes, "expected empty for %s", path)
	}
}

// ── route_tags <-> nsRouteCodes round-trip ────────────────────────────────────

func TestEdgeDNS_FindRecord_RouteTagsRoundtrip(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.",
		&iface.Record{ID: 5, Name: "cdn.example.com.", Type: mdns.TypeA, Value: "9.9.9.9", TTL: 60, RouteTags: "country=中国;isp=电信"},
	)))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/FindNSRecordWithNameAndType", map[string]any{
		"nsDomainId": domainID,
		"name":       "cdn",
		"type":       "A",
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	rec := data["nsRecord"].(map[string]any)
	routes := rec["nsRoutes"].([]any)
	require.Len(t, routes, 2)
	codes := map[string]bool{}
	for _, r := range routes {
		codes[r.(map[string]any)["code"].(string)] = true
	}
	assert.True(t, codes["country:中国"])
	assert.True(t, codes["isp:电信"])
}

func TestEdgeDNS_FindRecords_Multiple(t *testing.T) {
	zs := store.New()
	require.NoError(t, zs.Update(testutil.MakeZone("example.com.",
		&iface.Record{ID: 1, Name: "cdn.example.com.", Type: mdns.TypeA, Value: "1.1.1.1", TTL: 60},
		&iface.Record{ID: 2, Name: "cdn.example.com.", Type: mdns.TypeA, Value: "2.2.2.2", TTL: 60, RouteTags: "province=广东"},
		&iface.Record{ID: 3, Name: "mail.example.com.", Type: mdns.TypeA, Value: "3.3.3.3", TTL: 60},
	)))
	srv := newEdgeDNSSrv(&mockRS{}, zs)
	token := getToken(t, srv)
	domainID := findDomainID(t, srv, token, "example.com")

	rr := authed(t, srv, token, "/NSRecordService/FindNSRecordsWithNameAndType", map[string]any{
		"nsDomainId": domainID,
		"name":       "cdn",
		"type":       "A",
	})
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	records := data["nsRecords"].([]any)
	assert.Len(t, records, 2) // only cdn.example.com., not mail
}
