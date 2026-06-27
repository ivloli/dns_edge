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
// Handles two known xdb data versions:
//   - 4 fields: "国家|省份|城市|ISP"         (our current xdb)
//   - 5 fields: "国家|0|省份|城市|ISP" or "国家|省份|城市|中国电信|CN"
func parseRegion(raw string) GeoInfo {
	parts := strings.Split(raw, "|")
	var province, isp string
	switch len(parts) {
	case 4:
		// "中国|浙江省|绍兴市|电信"
		province = normalizeProvince(strings.TrimSpace(parts[1]))
		isp = normalizeISP(strings.TrimSpace(parts[3]))
	case 5:
		if strings.TrimSpace(parts[1]) == "0" {
			// "中国|0|广东省|广州市|电信"
			province = normalizeProvince(strings.TrimSpace(parts[2]))
			isp = normalizeISP(strings.TrimSpace(parts[4]))
		} else {
			// "中国|广东省|广州市|中国电信|CN"
			province = normalizeProvince(strings.TrimSpace(parts[1]))
			isp = normalizeISP(strings.TrimSpace(parts[3]))
		}
	default:
		return GeoInfo{}
	}
	country := strings.TrimSpace(parts[0])
	if country == "0" {
		country = ""
	}
	return GeoInfo{Country: country, Province: province, ISP: isp}
}

// normalizeProvince strips trailing "省" / "市".
func normalizeProvince(s string) string {
	s = strings.TrimSuffix(s, "省")
	s = strings.TrimSuffix(s, "市")
	s = strings.TrimSpace(s)
	if s == "0" {
		return ""
	}
	return s
}

// normalizeISP strips "中国" and "云" prefixes to match GoEdge route short names.
func normalizeISP(s string) string {
	s = strings.ReplaceAll(s, "中国", "")
	s = strings.ReplaceAll(s, "云", "")
	s = strings.TrimSpace(s)
	if s == "0" {
		return ""
	}
	return s
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
