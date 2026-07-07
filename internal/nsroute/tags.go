// Package nsroute converts GoEdge's route-code wire format ("province:上海",
// "isp:电信") to/from the internal route_tags format used by iface.Record
// ("province=上海;isp=电信"), shared between the CDN push path
// (internal/api) and the NS pull path (internal/edgeagent).
package nsroute

import "strings"

// CodesToTags converts GoEdge nsRouteCodes (["province:上海","isp:电信"])
// to the internal route_tags format ("province=上海;isp=电信").
func CodesToTags(codes []string) string {
	var parts []string
	for _, code := range codes {
		if code == "" || code == "default" {
			continue
		}
		if idx := strings.IndexByte(code, ':'); idx > 0 {
			parts = append(parts, code[:idx]+"="+code[idx+1:])
		}
	}
	return strings.Join(parts, ";")
}
