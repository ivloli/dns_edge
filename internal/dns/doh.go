package dns

import (
	"encoding/base64"
	"io"
	"math/rand"
	"net"
	"net/http"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/internal/metrics"
)

// dohContentType is the wire-format media type mandated by RFC 8484.
const dohContentType = "application/dns-message"

// maxDoHMessageSize caps request/response bodies. DNS messages carried over
// TCP/DoH aren't bound by UDP's 512-byte legacy limit, but an unbounded read
// is still a DoS surface; 64KiB matches the wire format's own 2-byte length
// prefix ceiling used elsewhere for TCP DNS and is far larger than any real
// query/answer this server produces.
const maxDoHMessageSize = 65535

// ServeDoH implements RFC 8484 (DNS over HTTPS) on top of the same query
// resolution path UDP/TCP use. It supports both transport encodings the RFC
// defines:
//   - GET  /dns-query?dns=<base64url, no padding>
//   - POST /dns-query  (Content-Type: application/dns-message, raw wire body)
//
// The core lookup logic (zone/geo/weight selection) lives entirely in
// handleQuery, which is transport-agnostic — this handler's only job is
// decoding the HTTP request into an *mdns.Msg, replicating the small amount
// of request-shaping ServeDNS does (EDNS0/ECS passthrough), and encoding the
// answer back as an HTTP response. It intentionally does not support
// AXFR/IXFR (zone transfer is a multi-message TCP-only operation with no
// sensible single-request/response HTTP mapping).
func (h *Handler) ServeDoH(w http.ResponseWriter, req *http.Request) {
	var wire []byte
	switch req.Method {
	case http.MethodGet:
		encoded := req.URL.Query().Get("dns")
		if encoded == "" {
			http.Error(w, "missing dns query parameter", http.StatusBadRequest)
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			http.Error(w, "invalid base64url dns parameter", http.StatusBadRequest)
			return
		}
		wire = decoded

	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(req.Body, maxDoHMessageSize+1))
		if err != nil {
			http.Error(w, "failed reading request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxDoHMessageSize {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		wire = body

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r := new(mdns.Msg)
	if err := r.Unpack(wire); err != nil {
		http.Error(w, "malformed dns message", http.StatusBadRequest)
		return
	}
	if len(r.Question) == 0 {
		http.Error(w, "dns message has no question", http.StatusBadRequest)
		return
	}
	q := r.Question[0]

	if q.Qtype == mdns.TypeAXFR || q.Qtype == mdns.TypeIXFR {
		http.Error(w, "zone transfer not supported over DoH", http.StatusNotImplemented)
		return
	}

	if h.syncer != nil && h.prob > 0 && rand.Float64() < h.prob {
		_ = h.syncer.TriggerSync()
	}

	start := time.Now()

	m := new(mdns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	// Same ECS passthrough ServeDNS does for UDP/TCP (handler.go:82-98) — a
	// DoH client can still carry an EDNS0 Client Subnet option in the wire
	// message even though the outer transport is HTTP, and geo-routing
	// downstream only cares about the parsed *mdns.Msg, not how it arrived.
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
					SourceScope:   0,
					Address:       ecs.Address,
				})
				break
			}
		}
	}

	h.log.Debug("doh query",
		zap.String("name", q.Name),
		zap.String("type", mdns.TypeToString[q.Qtype]),
	)

	h.handleQuery(m, r, q, clientIP)

	qtypeStr := mdns.TypeToString[q.Qtype]
	metrics.DNSQueriesTotal.WithLabelValues(qtypeStr, mdns.RcodeToString[m.Rcode]).Inc()
	metrics.DNSQueryDuration.WithLabelValues(qtypeStr).Observe(time.Since(start).Seconds())

	packed, err := m.Pack()
	if err != nil {
		http.Error(w, "failed to encode dns response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", dohContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(packed)
}
