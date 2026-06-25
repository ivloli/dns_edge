// Package dns implements the authoritative DNS query handler.
// It uses miekg/dns as the protocol layer (aliased as mdns to avoid
// collision with this package name).
package dns

import (
	"math/rand"
	"net"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/internal/geo"
	"dns-edge/internal/iface"
	"dns-edge/internal/metrics"
)

const axfrBatchSize = 500

// GeoLookup is the interface the Handler uses for IP → geo lookups.
// *geo.Router satisfies this interface.
type GeoLookup interface {
	Lookup(ip net.IP) geo.GeoInfo
}

// Handler answers authoritative DNS queries from ZoneStore.
// Traffic splitting is driven by WeightProvider (Phase 5: Nacos;
// Phase 1: Null — falls back to Record.Weight, then equal distribution).
// Geo-routing (Phase 13) filters records by RouteTags before weight selection.
type Handler struct {
	store   iface.ZoneStore
	weights iface.WeightProvider
	log     *zap.Logger
	syncer  iface.Syncer
	prob    float64
	geo     GeoLookup // nil = geo-routing disabled
}

// NewHandler constructs a Handler. Both store and weights must be non-nil.
// syncer may be nil (disables probabilistic sync trigger).
// geoRouter may be nil (disables geo-routing; all records are candidates).
func NewHandler(store iface.ZoneStore, weights iface.WeightProvider, syncer iface.Syncer, prob float64, log *zap.Logger, geoRouter GeoLookup) *Handler {
	return &Handler{store: store, weights: weights, syncer: syncer, prob: prob, log: log, geo: geoRouter}
}

// ServeDNS implements mdns.Handler. Called by the miekg/dns server on every query.
func (h *Handler) ServeDNS(w mdns.ResponseWriter, r *mdns.Msg) {
	if len(r.Question) == 0 {
		m := new(mdns.Msg)
		m.SetRcode(r, mdns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]

	// probabilistic incremental sync trigger (Phase 4)
	if h.syncer != nil && h.prob > 0 && rand.Float64() < h.prob {
		_ = h.syncer.TriggerSync()
	}

	// AXFR/IXFR — TCP-only zone transfer; handled separately (multi-message).
	if q.Qtype == mdns.TypeAXFR || q.Qtype == mdns.TypeIXFR {
		h.serveAXFR(w, r, q.Name)
		return
	}

	start := time.Now()

	m := new(mdns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	// EDNS0: reflect client's OPT back; advertise 4096-byte UDP buffer (RFC 6891).
	// If the query carries an ECS option (RFC 7871), extract clientIP and echo
	// it back with scope=0 (answer is not yet geo-specific; future geo-routing
	// will set SourceScope to the actual matched prefix length).
	var clientIP net.IP
	if opt := r.IsEdns0(); opt != nil {
		m.SetEdns0(4096, false)
		for _, o := range opt.Option {
			if ecs, ok := o.(*mdns.EDNS0_SUBNET); ok {
				clientIP = ecs.Address
				m.IsEdns0().Option = append(m.IsEdns0().Option, &mdns.EDNS0_SUBNET{
					Code:          mdns.EDNS0SUBNET,
					Family:        ecs.Family,
					SourceNetmask: ecs.SourceNetmask,
					SourceScope:   0, // scope=0: response not geo-specific yet
					Address:       ecs.Address,
				})
				break
			}
		}
	}

	h.log.Debug("query",
		zap.String("name", q.Name),
		zap.String("type", mdns.TypeToString[q.Qtype]),
	)

	h.handleQuery(m, r, q, clientIP)

	// Instrument after handleQuery so we capture the final rcode.
	qtypeStr := mdns.TypeToString[q.Qtype]
	metrics.DNSQueriesTotal.WithLabelValues(qtypeStr, mdns.RcodeToString[m.Rcode]).Inc()
	metrics.DNSQueryDuration.WithLabelValues(qtypeStr).Observe(time.Since(start).Seconds())

	_ = w.WriteMsg(m)
}

// handleQuery populates m based on q. It never calls w.WriteMsg — that is
// the responsibility of the caller (ServeDNS).
// clientIP is the ECS client address (nil when no ECS option was present).
func (h *Handler) handleQuery(m *mdns.Msg, r *mdns.Msg, q mdns.Question, clientIP net.IP) {
	// RFC 8482: answer TYPE=ANY with a minimal HINFO
	if q.Qtype == mdns.TypeANY {
		m.Answer = append(m.Answer, &mdns.HINFO{
			Hdr: mdns.RR_Header{
				Name: q.Name, Rrtype: mdns.TypeHINFO,
				Class: mdns.ClassINET, Ttl: 60,
			},
			Cpu: "RFC8482",
			Os:  "",
		})
		return
	}

	// Direct rrset lookup
	records := h.store.Lookup(q.Name, q.Qtype)
	if len(records) > 0 {
		h.addAnswers(m, records, q.Name, q.Qtype, clientIP)
		return
	}

	// CNAME chasing (single-hop) — skip if the query IS for CNAME
	if q.Qtype != mdns.TypeCNAME {
		cnames := h.store.Lookup(q.Name, mdns.TypeCNAME)
		if len(cnames) > 0 {
			cn := cnames[0]
			if cn.RR != nil {
				m.Answer = append(m.Answer, cn.RR)
			}
			target := ""
			if rr, ok := cn.RR.(*mdns.CNAME); ok {
				target = rr.Target
			}
			if target != "" {
				targeted := h.store.Lookup(target, q.Qtype)
				if len(targeted) > 0 {
					h.addAnswers(m, targeted, target, q.Qtype, clientIP)
				}
			}
			return
		}
	}

	// Find the authoritative zone for this name
	zone := h.store.FindZone(q.Name)
	if zone == nil {
		// Not authoritative — REFUSED
		m.SetRcode(r, mdns.RcodeRefused)
		return
	}

	if h.store.NameExists(q.Name) {
		// NODATA — name exists but wrong type
		m.SetRcode(r, mdns.RcodeSuccess)
		h.addSOA(m, zone)
		return
	}

	// NXDOMAIN — name does not exist in the zone
	m.SetRcode(r, mdns.RcodeNameError)
	h.addSOA(m, zone)
}

// addAnswers appends the rrset to the answer section.
// A and AAAA records are reduced to a single weighted-random pick;
// all other types are returned in full.
func (h *Handler) addAnswers(m *mdns.Msg, records []*iface.Record, fqdn string, qtype uint16, clientIP net.IP) {
	switch qtype {
	case mdns.TypeA, mdns.TypeAAAA:
		rec := h.pick(records, fqdn, qtype, clientIP)
		if rec != nil && rec.RR != nil {
			m.Answer = append(m.Answer, rec.RR)
		}
	default:
		for _, r := range records {
			if r.RR != nil {
				m.Answer = append(m.Answer, r.RR)
			}
		}
	}
}

// addSOA appends the zone SOA to the authority section (used for NODATA and
// NXDOMAIN responses).
func (h *Handler) addSOA(m *mdns.Msg, zone *iface.Zone) {
	m.Ns = append(m.Ns, h.syntheticSOA(zone))
}

// syntheticSOA returns the zone's stored SOA record, or synthesises a minimal
// one when the zone has no SOA stored (e.g. zones created via the API that
// have not yet been given a SOA record).
func (h *Handler) syntheticSOA(zone *iface.Zone) mdns.RR {
	if zone.SOA != nil {
		return zone.SOA
	}
	return &mdns.SOA{
		Hdr: mdns.RR_Header{
			Name:   zone.Name,
			Rrtype: mdns.TypeSOA,
			Class:  mdns.ClassINET,
			Ttl:    300,
		},
		Ns:      "ns1." + zone.Name,
		Mbox:    "hostmaster." + zone.Name,
		Serial:  1,
		Refresh: 3600,
		Retry:   900,
		Expire:  604800,
		Minttl:  300,
	}
}

// serveAXFR handles AXFR (and IXFR-as-AXFR) zone transfer requests.
// TCP-only; UDP requests are answered with REFUSED.
// Transfer format: SOA → records (≤500 per message) → SOA.
func (h *Handler) serveAXFR(w mdns.ResponseWriter, r *mdns.Msg, name string) {
	// AXFR requires TCP
	if _, isTCP := w.RemoteAddr().(*net.TCPAddr); !isTCP {
		m := new(mdns.Msg)
		m.SetRcode(r, mdns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}

	zone := h.store.FindZone(name)
	if zone == nil {
		m := new(mdns.Msg)
		m.SetRcode(r, mdns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}

	soaRR := h.syntheticSOA(zone)

	// Opening SOA
	open := new(mdns.Msg)
	open.SetReply(r)
	open.Authoritative = true
	open.Answer = []mdns.RR{soaRR}
	if err := w.WriteMsg(open); err != nil {
		return
	}

	// Records in batches
	batch := make([]mdns.RR, 0, axfrBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		msg := new(mdns.Msg)
		msg.SetReply(r)
		msg.Authoritative = true
		msg.Answer = batch
		batch = make([]mdns.RR, 0, axfrBatchSize)
		return w.WriteMsg(msg)
	}

	for _, recs := range zone.Records {
		for _, rec := range recs {
			if rec.RR == nil {
				continue
			}
			batch = append(batch, rec.RR)
			if len(batch) >= axfrBatchSize {
				if err := flush(); err != nil {
					return
				}
			}
		}
	}
	if err := flush(); err != nil {
		return
	}

	// Closing SOA
	close := new(mdns.Msg)
	close.SetReply(r)
	close.Authoritative = true
	close.Answer = []mdns.RR{soaRR}
	_ = w.WriteMsg(close)
}

// pick selects one record using weighted-random selection.
//
// Geo-routing (Phase 13): when a GeoRouter is configured and clientIP is
// non-nil, records whose RouteTags match the client's geo take priority.
// The candidate set is built as follows:
//  1. Records whose RouteTags match the client's geo (specific routes).
//  2. If no specific-route candidates exist, fall back to records with empty
//     RouteTags (default routes).
//  3. If neither exists, use all records.
//
// Weight priority: WeightProvider (dynamic) > Record.Weight (static) > 1.
func (h *Handler) pick(records []*iface.Record, fqdn string, qtype uint16, clientIP net.IP) *iface.Record {
	if len(records) == 1 {
		return records[0]
	}

	candidates := h.filterByGeo(records, clientIP)

	dynWeights := h.weights.GetWeights(fqdn, qtype, clientIP)

	total := 0
	ws := make([]int, len(candidates))
	for i, r := range candidates {
		w := r.Weight
		if dynWeights != nil {
			if dw, ok := dynWeights[r.Value]; ok {
				w = dw
			}
		}
		if w <= 0 {
			w = 1
		}
		ws[i] = w
		total += w
	}

	n := rand.Intn(total)
	for i, w := range ws {
		n -= w
		if n < 0 {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

// filterByGeo narrows records to those best matching the client's geo.
//
// Fallback chain (first non-empty tier wins):
//  1. Records whose RouteTags contain both the client's province and ISP
//  2. Records whose RouteTags contain the client's province only
//  3. Records whose RouteTags contain the client's ISP only
//  4. Records whose RouteTags contain the client's country only
//  5. Records with empty RouteTags (default route)
//  6. All records (last resort)
func (h *Handler) filterByGeo(records []*iface.Record, clientIP net.IP) []*iface.Record {
	if h.geo == nil || clientIP == nil {
		return records
	}

	info := h.geo.Lookup(clientIP)

	var provinceISP, province, isp, country, defaults []*iface.Record

	for _, r := range records {
		tags := r.RouteTags
		if tags == "" {
			defaults = append(defaults, r)
			continue
		}
		hasProvince := info.Province != "" && containsTag(tags, "province", info.Province)
		hasISP := info.ISP != "" && containsTag(tags, "isp", info.ISP)
		hasCountry := info.Country != "" && containsTag(tags, "country", info.Country)

		switch {
		case hasProvince && hasISP:
			provinceISP = append(provinceISP, r)
		case hasProvince:
			province = append(province, r)
		case hasISP:
			isp = append(isp, r)
		case hasCountry:
			country = append(country, r)
		}
	}

	for _, tier := range [][]*iface.Record{provinceISP, province, isp, country, defaults} {
		if len(tier) > 0 {
			return tier
		}
	}
	return records
}

// containsTag reports whether routeTags contains key=val as one of its
// semicolon-separated pairs. The check is a targeted single-dimension lookup,
// not a full Match — callers check each dimension independently.
func containsTag(routeTags, key, val string) bool {
	target := key + "=" + val
	for _, kv := range strings.Split(routeTags, ";") {
		if strings.TrimSpace(kv) == target {
			return true
		}
	}
	return false
}
