package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
)

type recordReq struct {
	Name      string `json:"name"       binding:"required"`
	Type      string `json:"type"       binding:"required"`
	TTL       uint32 `json:"ttl"        binding:"required,min=1"`
	Value     string `json:"value"      binding:"required"`
	Weight    int    `json:"weight"`
	RouteTags string `json:"route_tags"` // e.g. "country=中国;isp=电信;province=上海"; empty = default
}

type recordResp struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	TTL       uint32 `json:"ttl"`
	Value     string `json:"value"`
	Weight    int    `json:"weight"`
	RouteTags string `json:"route_tags"`
}

func toResp(r *iface.Record) recordResp {
	return recordResp{
		ID:        r.ID,
		Name:      r.Name,
		Type:      mdns.TypeToString[r.Type],
		TTL:       r.TTL,
		Value:     r.Value,
		Weight:    r.Weight,
		RouteTags: r.RouteTags,
	}
}

// GET /api/v1/domains/:domain/records
func (s *Server) listRecords(c *gin.Context) {
	apex := iface.FQDN(c.Param("domain"))

	recs, err := s.pg.ListRecords(c.Request.Context(), apex)
	if err != nil {
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	resp := make([]recordResp, len(recs))
	for i, r := range recs {
		resp[i] = toResp(r)
	}
	c.JSON(http.StatusOK, resp)
}

// POST /api/v1/domains/:domain/records
func (s *Server) createRecord(c *gin.Context) {
	apex := iface.FQDN(c.Param("domain"))

	var req recordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		s.jsonError(c, http.StatusBadRequest, err)
		return
	}

	qtype, ok := mdns.StringToType[req.Type]
	if !ok {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("unknown record type %q", req.Type))
		return
	}

	name := iface.FQDN(req.Name)
	rrStr := fmt.Sprintf("%s %d IN %s %s", name, req.TTL, req.Type, req.Value)
	rr, err := mdns.NewRR(rrStr)
	if err != nil {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("invalid rdata: %w", err))
		return
	}

	ctx := c.Request.Context()
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, fmt.Errorf("zone %q not found", apex))
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	rec := &iface.Record{Name: name, Type: qtype, TTL: req.TTL, Value: req.Value, Weight: req.Weight, RouteTags: req.RouteTags, RR: rr}
	created, isNew, err := s.pg.CreateRecord(ctx, zone.ID, rec)
	if err != nil {
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.PutRecord(apex, created); err != nil {
		s.log.Sugar().Warnf("zonestore PutRecord failed (PG write succeeded): %v", err)
	}

	status := http.StatusCreated
	if !isNew {
		status = http.StatusOK
	}
	c.JSON(status, toResp(created))
}

// PUT /api/v1/domains/:domain/records/:id
func (s *Server) updateRecord(c *gin.Context) {
	apex := iface.FQDN(c.Param("domain"))
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("invalid record id"))
		return
	}

	var req recordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		s.jsonError(c, http.StatusBadRequest, err)
		return
	}

	qtype, ok := mdns.StringToType[req.Type]
	if !ok {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("unknown record type %q", req.Type))
		return
	}

	name := iface.FQDN(req.Name)
	rrStr := fmt.Sprintf("%s %d IN %s %s", name, req.TTL, req.Type, req.Value)
	rr, err := mdns.NewRR(rrStr)
	if err != nil {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("invalid rdata: %w", err))
		return
	}

	ctx := c.Request.Context()
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, fmt.Errorf("zone %q not found", apex))
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	rec := &iface.Record{ID: id, Name: name, Type: qtype, TTL: req.TTL, Value: req.Value, Weight: req.Weight, RouteTags: req.RouteTags, RR: rr}
	updated, err := s.pg.UpdateRecord(ctx, zone.ID, id, rec)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, pg.ErrConflict) {
			s.jsonError(c, http.StatusConflict, err)
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.PutRecord(apex, updated); err != nil {
		s.log.Sugar().Warnf("zonestore PutRecord failed (PG write succeeded): %v", err)
	}

	c.JSON(http.StatusOK, toResp(updated))
}

// DELETE /api/v1/domains/:domain/records/:id
func (s *Server) deleteRecord(c *gin.Context) {
	apex := iface.FQDN(c.Param("domain"))
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		s.jsonError(c, http.StatusBadRequest, fmt.Errorf("invalid record id"))
		return
	}

	ctx := c.Request.Context()
	zone, err := s.pg.GetZone(ctx, apex)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, fmt.Errorf("zone %q not found", apex))
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	if err := s.pg.SoftDeleteRecord(ctx, zone.ID, id); err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, err)
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	_ = s.store.DropRecord(apex, id)

	c.Status(http.StatusNoContent)
}

// jsonError writes a JSON error body and sets the HTTP status.
func (s *Server) jsonError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}
