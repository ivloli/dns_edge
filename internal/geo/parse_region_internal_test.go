package geo

import "testing"

// These live in the internal (non-_test) package specifically to reach the
// unexported parseRegion — the public-API tests in geo_test.go can't call it
// directly, and this bug (see parseRegion's doc comment) is entirely about
// getting the field-splitting right, so it's worth pinning down exactly.

func TestParseRegion_4Fields(t *testing.T) {
	got := parseRegion("中国|浙江省|绍兴市|电信")
	want := GeoInfo{Country: "中国", Province: "浙江", ISP: "电信"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseRegion_5Fields_KnownProvince(t *testing.T) {
	got := parseRegion("中国|广东省|广州市|中国电信|CN")
	want := GeoInfo{Country: "中国", Province: "广东", ISP: "电信"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestParseRegion_5Fields_UnknownProvince pins down the actual bug: a real
// sample from this project's production xdb for an IP with unknown
// province/city ("中国|0|0|移动|CN") was being parsed as ISP="CN" (reading
// the trailing country-code field) instead of ISP="移动" (the real value,
// one field earlier). That silently broke isp-only route matching for any
// IP ip2region couldn't pin to a specific province/city.
func TestParseRegion_5Fields_UnknownProvince(t *testing.T) {
	got := parseRegion("中国|0|0|移动|CN")
	want := GeoInfo{Country: "中国", Province: "", ISP: "移动"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseRegion_UnknownFieldCount(t *testing.T) {
	got := parseRegion("中国|浙江省")
	want := GeoInfo{}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
