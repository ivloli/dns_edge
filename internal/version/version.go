// Package version holds dns-edge's build version, reported to edgeapi via
// UpdateNSNodeStatus so EdgeAdmin can detect nodes lagging behind the
// current release (mirrors how edge-node/edge-api already report their own
// version). dns-edge had no version concept before this — no git tags, no
// VERSION file — so this is a fresh starting point, not a migration of an
// existing scheme.
package version

import (
	"encoding/binary"
	"net"
	"strings"
)

// Version is dns-edge's build version. Bump this on every release that
// should be visible to EdgeAdmin's upgrade-detection banner.
const Version = "1.0.6"

// ToLong encodes a dotted version string (e.g. "1.0.0") as a comparable
// uint32 by zero-padding it to 4 segments and reading it as an IPv4
// address — the same trick edgenode/edgeapi already use for buildVersionCode,
// ported here so dns-edge can compute it locally before sending it in its
// heartbeat JSON.
func ToLong(version string) uint32 {
	var countDots = strings.Count(version, ".")
	if countDots == 2 {
		version += ".0"
	} else if countDots == 1 {
		version += ".0.0"
	} else if countDots == 0 {
		version += ".0.0.0"
	}
	var ip = net.ParseIP(version)
	if ip == nil || ip.To4() == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip.To4())
}
