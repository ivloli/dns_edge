package api

import (
	"context"
	"crypto/sha1" //nolint:gosec // GoEdge protocol mandates SHA-1
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
)

// goedgeRecord is the record object shape GoEdge exchanges in customHTTP requests/responses.
type goedgeRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Route string `json:"route"`
	TTL   uint32 `json:"ttl"`
}

// goedgeRoute is a DNS line/route entry returned by GetRoutes.
type goedgeRoute struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

type goedgeReq struct {
	Action     string        `json:"action"`
	Domain     string        `json:"domain"`
	Name       string        `json:"name"`
	RecordType string        `json:"recordType"`
	Record     *goedgeRecord `json:"record"`
	NewRecord  *goedgeRecord `json:"newRecord"`
}

// goedgeProvider handles POST /goedge/dns — the single endpoint for GoEdge's customHTTP provider.
func (s *Server) goedgeProvider(c *gin.Context) {
	if s.goedgeSecret != "" && !s.verifyGoEdgeToken(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req goedgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	switch req.Action {
	case "GetDomains":
		s.geGetDomains(c, ctx)
	case "GetRecords":
		s.geGetRecords(c, ctx, req.Domain)
	case "GetRoutes":
		c.JSON(http.StatusOK, []goedgeRoute{{Name: "默认", Code: "default"}})
	case "QueryRecord":
		s.geQueryRecord(c, ctx, req)
	case "QueryRecords":
		s.geQueryRecords(c, ctx, req)
	case "AddRecord":
		s.geAddRecord(c, ctx, req)
	case "UpdateRecord":
		s.geUpdateRecord(c, ctx, req)
	case "DeleteRecord":
		s.geDeleteRecord(c, ctx, req)
	case "DefaultRoute":
		// GoEdge expects a bare string, not JSON.
		c.String(http.StatusOK, "default")
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown action %q", req.Action)})
	}
}

// ── action handlers ───────────────────────────────────────────────────────────

func (s *Server) geGetDomains(c *gin.Context, ctx context.Context) {
	zones, err := s.pg.ListZones(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name
	}
	c.JSON(http.StatusOK, names)
}

func (s *Server) geGetRecords(c *gin.Context, ctx context.Context, domain string) {
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain required"})
		return
	}
	recs, err := s.pg.ListRecords(ctx, iface.FQDN(domain))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toGeRecords(recs))
}

func (s *Server) geQueryRecord(c *gin.Context, ctx context.Context, req goedgeReq) {
	recs, err := s.geListByNameType(ctx, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(recs) == 0 {
		c.JSON(http.StatusOK, nil)
		return
	}
	c.JSON(http.StatusOK, toGeRecord(recs[0]))
}

func (s *Server) geQueryRecords(c *gin.Context, ctx context.Context, req goedgeReq) {
	recs, err := s.geListByNameType(ctx, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(recs) == 0 {
		c.JSON(http.StatusOK, nil)
		return
	}
	c.JSON(http.StatusOK, toGeRecords(recs))
}

// geListByNameType fetches all records for (domain, name, recordType).
func (s *Server) geListByNameType(ctx context.Context, req goedgeReq) ([]*iface.Record, error) {
	all, err := s.pg.ListRecords(ctx, iface.FQDN(req.Domain))
	if err != nil {
		return nil, err
	}
	qtype, ok := mdns.StringToType[req.RecordType]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", req.RecordType)
	}
	name := iface.FQDN(req.Name)
	var out []*iface.Record
	for _, r := range all {
		if r.Name == name && r.Type == qtype {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Server) geAddRecord(c *gin.Context, ctx context.Context, req goedgeReq) {
	if req.NewRecord == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "newRecord required"})
		return
	}
	apex := iface.FQDN(req.Domain)
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		geZoneErr(c, apex, err)
		return
	}
	rec, err := geToRecord(req.NewRecord)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, _, err := s.pg.CreateRecord(ctx, zone.ID, rec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.store.PutRecord(apex, created)
	c.Status(http.StatusOK)
}

func (s *Server) geUpdateRecord(c *gin.Context, ctx context.Context, req goedgeReq) {
	if req.Record == nil || req.NewRecord == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "record and newRecord required"})
		return
	}
	apex := iface.FQDN(req.Domain)
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		geZoneErr(c, apex, err)
		return
	}
	id, err := strconv.ParseInt(req.Record.ID, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record id"})
		return
	}
	newRec, err := geToRecord(req.NewRecord)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := s.pg.UpdateRecord(ctx, zone.ID, id, newRec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.store.PutRecord(apex, updated)
	c.Status(http.StatusOK)
}

func (s *Server) geDeleteRecord(c *gin.Context, ctx context.Context, req goedgeReq) {
	if req.Record == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "record required"})
		return
	}
	apex := iface.FQDN(req.Domain)
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		geZoneErr(c, apex, err)
		return
	}
	id, err := strconv.ParseInt(req.Record.ID, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record id"})
		return
	}
	if err := s.pg.SoftDeleteRecord(ctx, zone.ID, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.store.DropRecord(apex, id)
	c.Status(http.StatusOK)
}

// ── auth ──────────────────────────────────────────────────────────────────────

// verifyGoEdgeToken validates GoEdge's Timestamp + Token headers.
// Token = lowercase-hex SHA1(secret + "@" + timestamp). Rejects replays > 5 min old.
func (s *Server) verifyGoEdgeToken(c *gin.Context) bool {
	ts := c.GetHeader("Timestamp")
	token := c.GetHeader("Token")
	if ts == "" || token == "" {
		return false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	diff := time.Now().Unix() - tsInt
	if diff < -300 || diff > 300 {
		return false
	}
	//nolint:gosec // SHA-1 required by GoEdge customHTTP spec
	h := sha1.New()
	h.Write([]byte(s.goedgeSecret + "@" + ts))
	return fmt.Sprintf("%x", h.Sum(nil)) == token
}

// ── conversion helpers ────────────────────────────────────────────────────────

func toGeRecord(r *iface.Record) goedgeRecord {
	route := r.RouteTags
	if route == "" {
		route = "default"
	}
	return goedgeRecord{
		ID:    strconv.FormatInt(r.ID, 10),
		Name:  r.Name,
		Type:  mdns.TypeToString[r.Type],
		Value: r.Value,
		Route: route,
		TTL:   r.TTL,
	}
}

func toGeRecords(recs []*iface.Record) []goedgeRecord {
	out := make([]goedgeRecord, len(recs))
	for i, r := range recs {
		out[i] = toGeRecord(r)
	}
	return out
}

// geToRecord converts a GoEdge record object to an internal iface.Record.
func geToRecord(gr *goedgeRecord) (*iface.Record, error) {
	qtype, ok := mdns.StringToType[gr.Type]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", gr.Type)
	}
	name := iface.FQDN(gr.Name)
	rrStr := fmt.Sprintf("%s %d IN %s %s", name, gr.TTL, gr.Type, gr.Value)
	rr, err := mdns.NewRR(rrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid rdata: %w", err)
	}
	routeTags := gr.Route
	if routeTags == "default" {
		routeTags = ""
	}
	return &iface.Record{Name: name, Type: qtype, TTL: gr.TTL, Value: gr.Value, RouteTags: routeTags, RR: rr}, nil
}

// geZoneErr writes an appropriate HTTP error for a GetZone failure.
func geZoneErr(c *gin.Context, apex string, err error) {
	if errors.Is(err, pg.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("zone %q not found", apex)})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
