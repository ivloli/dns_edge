package weight

import "dns-edge/internal/iface"

// CompositeWeightProvider delegates to primary, falling back to secondary
// when primary returns nil.
//
// Typical wiring: primary = NacosWeightProvider, secondary = StaticWeightProvider.
type CompositeWeightProvider struct {
	primary   iface.WeightProvider
	secondary iface.WeightProvider
}

var _ iface.WeightProvider = (*CompositeWeightProvider)(nil)

func NewComposite(primary, secondary iface.WeightProvider) *CompositeWeightProvider {
	return &CompositeWeightProvider{primary: primary, secondary: secondary}
}

func (p *CompositeWeightProvider) GetWeights(fqdn string, qtype uint16) map[string]int {
	if ws := p.primary.GetWeights(fqdn, qtype); ws != nil {
		return ws
	}
	return p.secondary.GetWeights(fqdn, qtype)
}
