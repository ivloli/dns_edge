package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
)

type createDomainReq struct {
	Name string `json:"name" binding:"required"`
}

type domainResp struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GET /api/v1/domains
func (s *Server) listDomains(c *gin.Context) {
	zones, err := s.pg.ListZones(c.Request.Context())
	if err != nil {
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}
	resp := make([]domainResp, len(zones))
	for i, z := range zones {
		resp[i] = domainResp{ID: z.ID, Name: z.Name}
	}
	c.JSON(http.StatusOK, resp)
}

// POST /api/v1/domains
func (s *Server) createDomain(c *gin.Context) {
	var req createDomainReq
	if err := c.ShouldBindJSON(&req); err != nil {
		s.jsonError(c, http.StatusBadRequest, err)
		return
	}

	apex := iface.FQDN(req.Name)
	meta, err := s.pg.CreateZone(c.Request.Context(), apex)
	if err != nil {
		if errors.Is(err, pg.ErrConflict) {
			s.jsonError(c, http.StatusConflict, err)
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	// put an empty zone into the in-memory store so NXDOMAIN works immediately
	_ = s.store.Update(&iface.Zone{
		Name:    apex,
		Records: make(map[iface.RecordKey][]*iface.Record),
	})

	c.JSON(http.StatusCreated, domainResp{ID: meta.ID, Name: meta.Name})
}

// DELETE /api/v1/domains/:domain
func (s *Server) deleteDomain(c *gin.Context) {
	apex := iface.FQDN(c.Param("domain"))

	if err := s.pg.SoftDeleteZone(c.Request.Context(), apex); err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			s.jsonError(c, http.StatusNotFound, err)
			return
		}
		s.jsonError(c, http.StatusInternalServerError, err)
		return
	}

	// best-effort: remove from in-memory store; DNS will 404 immediately
	_ = s.store.Delete(apex)

	c.Status(http.StatusNoContent)
}
