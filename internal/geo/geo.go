// Package geo wraps the ip2region xdb searcher and provides geo-routing
// tag matching for DNS record selection.
//
// A GeoInfo is parsed from ip2region's "国家|区域|省份|城市|ISP" result
// and matched against a record's RouteTags string
// (format: "country=中国;province=上海;isp=电信").
//
// Matching rules (all present tags must match; extra tags in RouteTags are
// ignored):
//
//	province=X  → geoInfo.Province == X
//	isp=X       → geoInfo.ISP == X
//	country=X   → geoInfo.Country == X
//
// Empty RouteTags means "default route" and always matches.
package geo

import (
	"net"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// GeoInfo holds the parsed fields returned by ip2region.
type GeoInfo struct {
	Country  string
	Province string
	ISP      string
}

// Router wraps an in-memory ip2region xdb searcher.
// Safe for concurrent use.
type Router struct {
	mu       sync.RWMutex
	searcher *xdb.Searcher
}

// New loads the xdb file entirely into memory and returns a Router.
// Call Close when the Router is no longer needed.
func New(xdbPath string) (*Router, error) {
	cBuff, err := xdb.LoadContentFromFile(xdbPath)
	if err != nil {
		return nil, err
	}
	header, err := xdb.LoadHeaderFromBuff(cBuff)
	if err != nil {
		return nil, err
	}
	ver, err := xdb.VersionFromHeader(header)
	if err != nil {
		return nil, err
	}
	searcher, err := xdb.NewWithBuffer(ver, cBuff)
	if err != nil {
		return nil, err
	}
	return &Router{searcher: searcher}, nil
}

// Close releases xdb resources.
func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.searcher != nil {
		r.searcher.Close()
		r.searcher = nil
	}
}

// Lookup returns geographic information for ip.
// Returns zero-value GeoInfo (all empty) on any error or when ip is nil.
func (r *Router) Lookup(ip net.IP) GeoInfo {
	if ip == nil {
		return GeoInfo{}
	}
	r.mu.RLock()
	s := r.searcher
	r.mu.RUnlock()
	if s == nil {
		return GeoInfo{}
	}
	raw, err := s.Search(ip.String())
	if err != nil {
		return GeoInfo{}
	}
	return parseRegion(raw)
}

// parseRegion parses ip2region's pipe-separated result.
// Actual format (4 fields): "国家|省份|城市|ISP"
// Fields beyond index 3 are ignored.
func parseRegion(raw string) GeoInfo {
	parts := strings.SplitN(raw, "|", 5)
	get := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		v := strings.TrimSpace(parts[i])
		if v == "0" || v == "" {
			return ""
		}
		return v
	}
	return GeoInfo{
		Country:  get(0),
		Province: normalizeProvince(get(1)),
		ISP:      get(3),
	}
}

// normalizeProvince strips trailing administrative suffixes so "浙江省" → "浙江",
// "内蒙古自治区" → "内蒙古", matching the short names GoEdge uses in route codes.
func normalizeProvince(p string) string {
	for _, suffix := range []string{"省", "市", "自治区", "特别行政区"} {
		if strings.HasSuffix(p, suffix) {
			return p[:len(p)-len(suffix)]
		}
	}
	return p
}

// Match reports whether geo matches routeTags.
//
// routeTags format: "country=中国;province=上海;isp=电信" (semicolon-separated).
// Empty routeTags always matches (default route).
// Unknown tag dimensions are ignored.
func (g GeoInfo) Match(routeTags string) bool {
	if routeTags == "" {
		return true
	}
	for _, kv := range strings.Split(routeTags, ";") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key := kv[:eq]
		val := kv[eq+1:]
		switch key {
		case "country":
			if g.Country != val {
				return false
			}
		case "province":
			if g.Province != val {
				return false
			}
		case "isp":
			if g.ISP != val {
				return false
			}
		}
	}
	return true
}
