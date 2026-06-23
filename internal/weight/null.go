package weight

import (
	"net"

	"dns-edge/internal/iface"
)

// Null is the Phase-1 WeightProvider.
// It always returns nil, causing the DNS handler to fall back to
// Record.Weight (static) and then to equal distribution.
//
// Replace with NacosWeightProvider in Phase 5.
type Null struct{}

// Compile-time interface check.
var _ iface.WeightProvider = Null{}

func (Null) GetWeights(_ string, _ uint16, _ net.IP) map[string]int { return nil }
