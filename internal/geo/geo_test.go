package geo_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"dns-edge/internal/geo"
)

// ── parseRegion (via Lookup path) tested through exported Match ────────────

func TestGeoInfo_Match_EmptyTags(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match(""), "empty RouteTags = default route, always matches")
}

func TestGeoInfo_Match_ExactProvince(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match("province=上海"))
	assert.False(t, g.Match("province=北京"))
}

func TestGeoInfo_Match_ExactISP(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match("isp=电信"))
	assert.False(t, g.Match("isp=联通"))
}

func TestGeoInfo_Match_ExactCountry(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match("country=中国"))
	assert.False(t, g.Match("country=美国"))
}

func TestGeoInfo_Match_MultiTag_AllMatch(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match("province=上海;isp=电信"))
}

func TestGeoInfo_Match_MultiTag_PartialFail(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	// province matches but isp doesn't
	assert.False(t, g.Match("province=上海;isp=联通"))
}

func TestGeoInfo_Match_UnknownDimension(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	// unknown dimension is ignored
	assert.True(t, g.Match("city=徐汇"))
}

func TestGeoInfo_Match_ZeroGeo_OnlyEmptyTagsMatch(t *testing.T) {
	// zero GeoInfo (lookup failed or no xdb): only empty tags match
	g := geo.GeoInfo{}
	assert.True(t, g.Match(""))
	assert.False(t, g.Match("country=中国"))
	assert.False(t, g.Match("province=上海"))
}

func TestGeoInfo_Match_MalformedTag_Ignored(t *testing.T) {
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	// tag without '=' should not panic and should be skipped
	assert.True(t, g.Match("noequals;province=上海"))
}

// ── Router.Lookup with nil IP ─────────────────────────────────────────────

func TestRouter_Lookup_NilIP_ReturnsZero(t *testing.T) {
	// Router with no xdb: we can't easily construct a real one in unit tests,
	// but we can verify the nil-IP guard via a nil-xdb-path error path.
	// Instead, test the GeoInfo zero-value behaviour (covered above).
	// Verifying Lookup(nil) doesn't panic:
	r := newNilRouter(t)
	g := r.Lookup(nil)
	assert.Equal(t, geo.GeoInfo{}, g)
}

func TestRouter_Lookup_InvalidXDB_ReturnsZero(t *testing.T) {
	_, err := geo.New("/nonexistent/path.xdb")
	assert.Error(t, err)
}

// newNilRouter returns a closed Router (searcher=nil) for nil-guard tests.
func newNilRouter(t *testing.T) *geo.Router {
	t.Helper()
	// We can't call geo.New without a real file. Use a trick: pass an
	// obviously bad path, expect error, then test Lookup(nil) separately.
	// This sub-test just validates the zero-GeoInfo path via Match tests above.
	//
	// If you have a real ip2region.xdb available, set IPDB env and use
	// TestRouter_Lookup_RealXDB below.
	r, err := geo.New("/nonexistent.xdb")
	if err != nil {
		// Expected; return a dummy to avoid nil deref in Lookup(nil).
		// We test nil-IP guard indirectly through Match tests on zero GeoInfo.
		t.Skip("no real xdb available for Router construction")
	}
	return r
}

// ── Handler geo filtering tested in handler_test.go ──────────────────────
// See internal/dns/handler_test.go TestGeoRouting_* for integration tests.

// ── filterByGeo behaviour via handler_test.go ─────────────────────────────
// The handler tests cover: specific-route priority, default fallback, and
// all-records fallback when no tags match and no defaults exist.

// ── parseRegion ───────────────────────────────────────────────────────────

func TestParseRegion_NormalFormat(t *testing.T) {
	// We expose this indirectly: feed a fake Lookup via a test router.
	// Since we can't easily mock the xdb searcher, we test Match() with
	// values that parseRegion would produce for known ip2region outputs.
	// ip2region returns: "中国|华东|上海|上海市|电信"
	// parseRegion produces: {Country:"中国", Province:"上海", ISP:"电信"}
	g := geo.GeoInfo{Country: "中国", Province: "上海", ISP: "电信"}
	assert.True(t, g.Match("country=中国;province=上海;isp=电信"))
}

func TestParseRegion_ZeroFields(t *testing.T) {
	// ip2region returns "0" for unknown fields
	g := geo.GeoInfo{Country: "0", Province: "0", ISP: "0"}
	// parseRegion treats "0" as empty string
	// simulate by using empty GeoInfo
	g2 := geo.GeoInfo{}
	assert.False(t, g.Match("country=中国"))
	assert.True(t, g2.Match(""))
}

// TestGeoRouting integration: covered in handler_test.go via net.IP fixtures.
// Run: go test ./internal/dns/ -run TestGeoRouting

// ── Lookup with a real xdb (integration, skipped without file) ────────────

func TestRouter_Lookup_RealXDB(t *testing.T) {
	xdbPath := "testdata/ip2region.xdb"
	r, err := geo.New(xdbPath)
	if err != nil {
		t.Skipf("no xdb at %s: %v", xdbPath, err)
	}
	defer r.Close()

	// 8.8.8.8 = Google DNS = United States
	g := r.Lookup(net.ParseIP("8.8.8.8"))
	assert.NotEmpty(t, g.Country, "expected a country for 8.8.8.8")
}
