package api_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// ── helpers ───────────────────────────────────────────────────────────────────

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
	srv := newEdgeDNSSrv(&mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return nil, nil
		},
	}, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	// same token works for multiple requests
	for i := 0; i < 3; i++ {
		rr := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{})
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, float64(200), edgeDNSCode(t, rr))
	}
}

// ── NSDomainService ───────────────────────────────────────────────────────────

func TestEdgeDNS_ListDomains(t *testing.T) {
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{
				{ID: 1, Name: "example.com."},
				{ID: 2, Name: "foo.org."},
			}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSDomainService/ListNSDomains", map[string]any{"offset": 0, "size": 10})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	domains := data["nsDomains"].([]any)
	require.Len(t, domains, 2)
	// trailing dot stripped in response
	assert.Equal(t, "example.com", domains[0].(map[string]any)["name"])
}

func TestEdgeDNS_ListDomains_Pagination(t *testing.T) {
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{
				{ID: 1, Name: "a.com."},
				{ID: 2, Name: "b.com."},
				{ID: 3, Name: "c.com."},
			}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
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
	rs := &mockRS{
		getZoneFn: func(_ context.Context, apex string) (iface.ZoneMeta, error) {
			return iface.ZoneMeta{ID: 5, Name: apex}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSDomainService/FindNSDomainWithName", map[string]any{"name": "example.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	domain := data["nsDomain"].(map[string]any)
	assert.Equal(t, float64(5), domain["id"])
}

func TestEdgeDNS_FindDomain_NotFound(t *testing.T) {
	rs := &mockRS{
		getZoneFn: func(_ context.Context, _ string) (iface.ZoneMeta, error) {
			return iface.ZoneMeta{}, pg.ErrNotFound
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSDomainService/FindNSDomainWithName", map[string]any{"name": "ghost.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, float64(200), resp["code"])
	data := resp["data"].(map[string]any)
	assert.Nil(t, data["nsDomain"])
}

// ── NSRecordService ───────────────────────────────────────────────────────────

func TestEdgeDNS_ListRecords(t *testing.T) {
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		listRecordsFn: func(_ context.Context, _ string) ([]*iface.Record, error) {
			return []*iface.Record{
				{ID: 10, Name: "www.example.com.", Type: 1, Value: "1.2.3.4", TTL: 300},
				{ID: 11, Name: "www.example.com.", Type: 1, Value: "5.6.7.8", TTL: 300, RouteTags: "province=上海"},
			}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/ListNSRecords", map[string]any{"nsDomainId": 1, "offset": 0, "size": 10})
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	records := data["nsRecords"].([]any)
	require.Len(t, records, 2)
	// second record has province route — verify route_tags → nsRoutes conversion
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
	var capturedTags string
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			capturedTags = rec.RouteTags
			rec.ID = 77
			return rec, true, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   1,
		"name":         "www.example.com",
		"type":         "A",
		"value":        "1.2.3.4",
		"ttl":          300,
		"nsRouteCodes": []string{"default"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(200), edgeDNSCode(t, rr))
	assert.Equal(t, "", capturedTags) // "default" -> empty route_tags
}

func TestEdgeDNS_CreateRecord_ProvinceAndISP(t *testing.T) {
	var capturedTags string
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			capturedTags = rec.RouteTags
			rec.ID = 78
			return rec, true, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   1,
		"name":         "www.example.com",
		"type":         "A",
		"value":        "2.2.2.2",
		"ttl":          300,
		"nsRouteCodes": []string{"province:上海", "isp:电信"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	// nsRouteCodes -> route_tags: "province:上海;isp:电信" stored as "province=上海;isp=电信"
	assert.Equal(t, "province=上海;isp=电信", capturedTags)
}

func TestEdgeDNS_CreateRecord_CountryRoute(t *testing.T) {
	var capturedTags string
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		createRecordFn: func(_ context.Context, _ int64, rec *iface.Record) (*iface.Record, bool, error) {
			capturedTags = rec.RouteTags
			rec.ID = 79
			return rec, true, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   1,
		"name":         "cdn.example.com",
		"type":         "A",
		"value":        "3.3.3.3",
		"ttl":          300,
		"nsRouteCodes": []string{"country:中国"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "country=中国", capturedTags)
}

func TestEdgeDNS_CreateRecord_ZoneNotFound(t *testing.T) {
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return nil, nil // empty -> zone 999 not found
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/CreateNSRecord", map[string]any{
		"nsDomainId":   999,
		"name":         "www.example.com",
		"type":         "A",
		"value":        "1.1.1.1",
		"ttl":          300,
		"nsRouteCodes": []string{},
	})
	assert.Equal(t, float64(404), edgeDNSCode(t, rr))
}

func TestEdgeDNS_DeleteRecord_Success(t *testing.T) {
	var deletedID int64
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		listRecordsFn: func(_ context.Context, _ string) ([]*iface.Record, error) {
			return []*iface.Record{{ID: 99, Name: "www.example.com.", Type: 1, Value: "1.1.1.1", TTL: 300}}, nil
		},
		softDeleteRecordFn: func(_ context.Context, _, id int64) error {
			deletedID = id
			return nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/DeleteNSRecord", map[string]any{"nsRecordId": 99})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, float64(200), edgeDNSCode(t, rr))
	assert.Equal(t, int64(99), deletedID)
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
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		listRecordsFn: func(_ context.Context, _ string) ([]*iface.Record, error) {
			return []*iface.Record{
				{ID: 5, Name: "cdn.example.com.", Type: 1, Value: "9.9.9.9", TTL: 60, RouteTags: "country=中国;isp=电信"},
			}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/FindNSRecordWithNameAndType", map[string]any{
		"nsDomainId": 1,
		"name":       "cdn.example.com",
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
	rs := &mockRS{
		listZonesFn: func(_ context.Context) ([]iface.ZoneMeta, error) {
			return []iface.ZoneMeta{{ID: 1, Name: "example.com."}}, nil
		},
		listRecordsFn: func(_ context.Context, _ string) ([]*iface.Record, error) {
			return []*iface.Record{
				{ID: 1, Name: "cdn.example.com.", Type: 1, Value: "1.1.1.1", TTL: 60},
				{ID: 2, Name: "cdn.example.com.", Type: 1, Value: "2.2.2.2", TTL: 60, RouteTags: "province=广东"},
				{ID: 3, Name: "mail.example.com.", Type: 1, Value: "3.3.3.3", TTL: 60},
			}, nil
		},
	}
	srv := newEdgeDNSSrv(rs, &testutil.MockZoneStore{})
	token := getToken(t, srv)
	rr := authed(t, srv, token, "/NSRecordService/FindNSRecordsWithNameAndType", map[string]any{
		"nsDomainId": 1,
		"name":       "cdn.example.com",
		"type":       "A",
	})
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	data := resp["data"].(map[string]any)
	records := data["nsRecords"].([]any)
	assert.Len(t, records, 2) // only cdn.example.com., not mail
}
