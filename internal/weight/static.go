package weight

import (
	"net"

	"dns-edge/internal/iface"
)

// StaticWeightProvider reads per-record weights directly from ZoneStore
// (Record.Weight). Used as the fallback when NacosWeightProvider is
// unavailable or returns no weights for a given name.
type StaticWeightProvider struct {
	store iface.ZoneStore
}

var _ iface.WeightProvider = (*StaticWeightProvider)(nil)

func NewStatic(store iface.ZoneStore) *StaticWeightProvider {
	return &StaticWeightProvider{store: store}
}

// GetWeights returns the static weights for (fqdn, qtype) from ZoneStore.
// clientIP is reserved for future geo-routing; currently ignored.
// Returns nil when all records have Weight == 0 (equal distribution).
func (p *StaticWeightProvider) GetWeights(fqdn string, qtype uint16, _ net.IP) map[string]int {
	records := p.store.Lookup(fqdn, qtype)
	ws := make(map[string]int, len(records))
	for _, r := range records {
		if r.Weight > 0 {
			ws[r.Value] = r.Weight
		}
	}
	if len(ws) == 0 {
		return nil
	}
	return ws
}
