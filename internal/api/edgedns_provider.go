package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
)

// ── static route tables ───────────────────────────────────────────────────────

type nsRoute struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

var (
	worldRegionRoutes = []nsRoute{
		{Name: "中国", Code: "country:中国"},
		{Name: "美国", Code: "country:美国"},
		{Name: "日本", Code: "country:日本"},
		{Name: "韩国", Code: "country:韩国"},
		{Name: "香港", Code: "country:香港"},
		{Name: "台湾", Code: "country:台湾"},
		{Name: "新加坡", Code: "country:新加坡"},
		{Name: "英国", Code: "country:英国"},
		{Name: "德国", Code: "country:德国"},
		{Name: "法国", Code: "country:法国"},
		{Name: "澳大利亚", Code: "country:澳大利亚"},
	}

	chinaProvinceRoutes = []nsRoute{
		{Name: "北京", Code: "province:北京"},
		{Name: "天津", Code: "province:天津"},
		{Name: "河北", Code: "province:河北"},
		{Name: "山西", Code: "province:山西"},
		{Name: "内蒙古", Code: "province:内蒙古"},
		{Name: "辽宁", Code: "province:辽宁"},
		{Name: "吉林", Code: "province:吉林"},
		{Name: "黑龙江", Code: "province:黑龙江"},
		{Name: "上海", Code: "province:上海"},
		{Name: "江苏", Code: "province:江苏"},
		{Name: "浙江", Code: "province:浙江"},
		{Name: "安徽", Code: "province:安徽"},
		{Name: "福建", Code: "province:福建"},
		{Name: "江西", Code: "province:江西"},
		{Name: "山东", Code: "province:山东"},
		{Name: "河南", Code: "province:河南"},
		{Name: "湖北", Code: "province:湖北"},
		{Name: "湖南", Code: "province:湖南"},
		{Name: "广东", Code: "province:广东"},
		{Name: "广西", Code: "province:广西"},
		{Name: "海南", Code: "province:海南"},
		{Name: "重庆", Code: "province:重庆"},
		{Name: "四川", Code: "province:四川"},
		{Name: "贵州", Code: "province:贵州"},
		{Name: "云南", Code: "province:云南"},
		{Name: "西藏", Code: "province:西藏"},
		{Name: "陕西", Code: "province:陕西"},
		{Name: "甘肃", Code: "province:甘肃"},
		{Name: "青海", Code: "province:青海"},
		{Name: "宁夏", Code: "province:宁夏"},
		{Name: "新疆", Code: "province:新疆"},
	}

	ispRoutes = []nsRoute{
		{Name: "电信", Code: "isp:电信"},
		{Name: "联通", Code: "isp:联通"},
		{Name: "移动", Code: "isp:移动"},
		{Name: "教育网", Code: "isp:教育网"},
	}
)

// ── request/response types ────────────────────────────────────────────────────

type getTokenReq struct {
	Type        string `json:"type"`
	AccessKeyID string `json:"accessKeyId"`
	AccessKey   string `json:"accessKey"`
}

type nsRecordObj struct {
	Id       int64      `json:"id"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Value    string     `json:"value"`
	TTL      uint32     `json:"ttl"`
	IsOn     bool       `json:"isOn"`
	NSRoutes []nsRoute  `json:"nsRoutes"`
}

type nsDomainObj struct {
	Id        int64  `json:"id"`
	Name      string `json:"name"`
	IsOn      bool   `json:"isOn"`
	IsDeleted bool   `json:"isDeleted"`
}

// ── registerEdgeDNSRoutes wires all edgeDNSAPI endpoints onto r ───────────────

func (s *Server) registerEdgeDNSRoutes(r gin.IRouter) {
	// Token endpoint — no auth required.
	r.POST("/APIAccessTokenService/getAPIAccessToken", s.edgeDNSGetToken)

	// All other endpoints require a valid token.
	authed := r.Group("/", s.edgeDNSAuth.requireToken())
	authed.POST("/NSDomainService/ListNSDomains", s.edgeDNSListDomains)
	authed.POST("/NSDomainService/FindNSDomainWithName", s.edgeDNSFindDomain)

	authed.POST("/NSRecordService/ListNSRecords", s.edgeDNSListRecords)
	authed.POST("/NSRecordService/FindNSRecordWithNameAndType", s.edgeDNSFindRecord)
	authed.POST("/NSRecordService/FindNSRecordsWithNameAndType", s.edgeDNSFindRecords)
	authed.POST("/NSRecordService/CreateNSRecord", s.edgeDNSCreateRecord)
	authed.POST("/NSRecordService/UpdateNSRecord", s.edgeDNSUpdateRecord)
	authed.POST("/NSRecordService/DeleteNSRecord", s.edgeDNSDeleteRecord)

	authed.POST("/NSRouteService/FindAllDefaultWorldRegionRoutes", s.edgeDNSWorldRegionRoutes)
	authed.POST("/NSRouteService/FindAllDefaultChinaProvinceRoutes", s.edgeDNSChinaProvinceRoutes)
	authed.POST("/NSRouteService/FindAllDefaultISPRoutes", s.edgeDNSISPRoutes)
	authed.POST("/NSRouteService/FindAllAgentNSRoutes", s.edgeDNSAgentRoutes)
	authed.POST("/NSRouteService/FindAllNSRoutes", s.edgeDNSCustomRoutes)
}

// ── APIAccessTokenService ─────────────────────────────────────────────────────

func (s *Server) edgeDNSGetToken(c *gin.Context) {
	if !s.edgeDNSAuth.enabled() {
		c.JSON(http.StatusNotFound, edgeDNSErrResp(404, "edgeDNSAPI not configured"))
		return
	}
	var req getTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}
	if req.AccessKeyID != s.edgeDNSAuth.keyID || req.AccessKey != s.edgeDNSAuth.keySecret {
		c.JSON(http.StatusOK, edgeDNSErrResp(401, "invalid accessKeyId or accessKey"))
		return
	}
	token, expiresAt, err := s.edgeDNSAuth.issueToken()
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{
		"token":     token,
		"expiresAt": expiresAt,
	}))
}

// ── NSDomainService ───────────────────────────────────────────────────────────

func (s *Server) edgeDNSListDomains(c *gin.Context) {
	var req struct {
		Offset int `json:"offset"`
		Size   int `json:"size"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Size <= 0 {
		req.Size = 100
	}

	zones, err := s.pg.ListZones(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}

	// apply offset/size pagination
	if req.Offset >= len(zones) {
		zones = nil
	} else {
		zones = zones[req.Offset:]
		if req.Size < len(zones) {
			zones = zones[:req.Size]
		}
	}

	domains := make([]nsDomainObj, len(zones))
	for i, z := range zones {
		domains[i] = nsDomainObj{Id: z.ID, Name: strings.TrimSuffix(z.Name, "."), IsOn: true}
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsDomains": domains}))
}

func (s *Server) edgeDNSFindDomain(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, "name required"))
		return
	}
	zone, err := s.pg.GetZone(c.Request.Context(), iface.FQDN(req.Name))
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsDomain": nil}))
			return
		}
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsDomain": nsDomainObj{
		Id: zone.ID, Name: strings.TrimSuffix(zone.Name, "."), IsOn: true,
	}}))
}

// ── NSRecordService ───────────────────────────────────────────────────────────

func (s *Server) edgeDNSListRecords(c *gin.Context) {
	var req struct {
		NSDomainID int64 `json:"nsDomainId"`
		Offset     int   `json:"offset"`
		Size       int   `json:"size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NSDomainID == 0 {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, "nsDomainId required"))
		return
	}
	if req.Size <= 0 {
		req.Size = 100
	}

	apex, err := s.resolveApex(c.Request.Context(), req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	recs, err := s.pg.ListRecords(c.Request.Context(), apex)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}

	if req.Offset >= len(recs) {
		recs = nil
	} else {
		recs = recs[req.Offset:]
		if req.Size < len(recs) {
			recs = recs[:req.Size]
		}
	}

	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecords": toNSRecordObjs(recs)}))
}

func (s *Server) edgeDNSFindRecord(c *gin.Context) {
	var req struct {
		NSDomainID int64  `json:"nsDomainId"`
		Name       string `json:"name"`
		Type       string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}
	recs, err := s.edgeDNSQueryByNameType(c.Request.Context(), req.NSDomainID, req.Name, req.Type)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	if len(recs) == 0 {
		c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecord": nil}))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecord": toNSRecordObj(recs[0])}))
}

func (s *Server) edgeDNSFindRecords(c *gin.Context) {
	var req struct {
		NSDomainID int64  `json:"nsDomainId"`
		Name       string `json:"name"`
		Type       string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}
	recs, err := s.edgeDNSQueryByNameType(c.Request.Context(), req.NSDomainID, req.Name, req.Type)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecords": toNSRecordObjs(recs)}))
}

func (s *Server) edgeDNSCreateRecord(c *gin.Context) {
	var req struct {
		NSDomainID   int64    `json:"nsDomainId"`
		Name         string   `json:"name"`
		Type         string   `json:"type"`
		Value        string   `json:"value"`
		TTL          uint32   `json:"ttl"`
		NSRouteCodes []string `json:"nsRouteCodes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}

	apex, err := s.resolveApex(c.Request.Context(), req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	rec, err := nsToRecord(req.Name, req.Type, req.Value, req.TTL, req.NSRouteCodes)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}

	created, _, err := s.pg.CreateRecord(c.Request.Context(), req.NSDomainID, rec)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	_ = s.store.PutRecord(apex, created)
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecordId": created.ID}))
}

func (s *Server) edgeDNSUpdateRecord(c *gin.Context) {
	var req struct {
		NSRecordID   int64    `json:"nsRecordId"`
		Name         string   `json:"name"`
		Type         string   `json:"type"`
		Value        string   `json:"value"`
		TTL          uint32   `json:"ttl"`
		NSRouteCodes []string `json:"nsRouteCodes"`
		IsOn         bool     `json:"isOn"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NSRecordID == 0 {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, "nsRecordId required"))
		return
	}

	rec, err := nsToRecord(req.Name, req.Type, req.Value, req.TTL, req.NSRouteCodes)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}

	// We need the zone ID; look it up by listing zones and finding the record.
	// Simpler: reuse pg.UpdateRecord which takes (zoneID, recordID). We find
	// the zone by doing a ListZones + matching record lookup. Since UpdateRecord
	// only needs zoneID for scoping, we fetch it via a dedicated lookup.
	zoneID, apex, err := s.findZoneForRecord(c.Request.Context(), req.NSRecordID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	updated, err := s.pg.UpdateRecord(c.Request.Context(), zoneID, req.NSRecordID, rec)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	_ = s.store.PutRecord(apex, updated)
	c.JSON(http.StatusOK, edgeDNSOK(nil))
}

func (s *Server) edgeDNSDeleteRecord(c *gin.Context) {
	var req struct {
		NSRecordID int64 `json:"nsRecordId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NSRecordID == 0 {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, "nsRecordId required"))
		return
	}

	zoneID, apex, err := s.findZoneForRecord(c.Request.Context(), req.NSRecordID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	if err := s.pg.SoftDeleteRecord(c.Request.Context(), zoneID, req.NSRecordID); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	_ = s.store.DropRecord(apex, req.NSRecordID)
	c.JSON(http.StatusOK, edgeDNSOK(nil))
}

// ── NSRouteService ────────────────────────────────────────────────────────────

func (s *Server) edgeDNSWorldRegionRoutes(c *gin.Context) {
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRoutes": worldRegionRoutes}))
}

func (s *Server) edgeDNSChinaProvinceRoutes(c *gin.Context) {
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRoutes": chinaProvinceRoutes}))
}

func (s *Server) edgeDNSISPRoutes(c *gin.Context) {
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRoutes": ispRoutes}))
}

func (s *Server) edgeDNSAgentRoutes(c *gin.Context) {
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRoutes": []nsRoute{}}))
}

func (s *Server) edgeDNSCustomRoutes(c *gin.Context) {
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRoutes": []nsRoute{}}))
}

// ── internal helpers ──────────────────────────────────────────────────────────

// resolveApex looks up the zone apex FQDN by zone ID (via ListZones).
func (s *Server) resolveApex(ctx context.Context, zoneID int64) (string, error) {
	zones, err := s.pg.ListZones(ctx)
	if err != nil {
		return "", err
	}
	for _, z := range zones {
		if z.ID == zoneID {
			return z.Name, nil
		}
	}
	return "", fmt.Errorf("zone %d not found", zoneID)
}

// findZoneForRecord returns (zoneID, apex) by scanning all zones for the record.
func (s *Server) findZoneForRecord(ctx context.Context, recordID int64) (int64, string, error) {
	zones, err := s.pg.ListZones(ctx)
	if err != nil {
		return 0, "", err
	}
	for _, z := range zones {
		recs, err := s.pg.ListRecords(ctx, z.Name)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.ID == recordID {
				return z.ID, z.Name, nil
			}
		}
	}
	return 0, "", fmt.Errorf("record %d not found", recordID)
}

// edgeDNSQueryByNameType fetches records matching (domainID, name, type).
func (s *Server) edgeDNSQueryByNameType(ctx context.Context, domainID int64, name, recType string) ([]*iface.Record, error) {
	apex, err := s.resolveApex(ctx, domainID)
	if err != nil {
		return nil, err
	}
	all, err := s.pg.ListRecords(ctx, apex)
	if err != nil {
		return nil, err
	}
	qtype, ok := mdns.StringToType[strings.ToUpper(recType)]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", recType)
	}
	fqdn := iface.FQDN(name)
	var out []*iface.Record
	for _, r := range all {
		if r.Name == fqdn && r.Type == qtype {
			out = append(out, r)
		}
	}
	return out, nil
}

// nsToRecord converts edgeDNSAPI fields to an iface.Record.
func nsToRecord(name, recType, value string, ttl uint32, nsRouteCodes []string) (*iface.Record, error) {
	qtype, ok := mdns.StringToType[strings.ToUpper(recType)]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", recType)
	}
	fqdn := iface.FQDN(name)
	rrStr := fmt.Sprintf("%s %d IN %s %s", fqdn, ttl, strings.ToUpper(recType), value)
	rr, err := mdns.NewRR(rrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid rdata: %w", err)
	}
	return &iface.Record{
		Name:      fqdn,
		Type:      qtype,
		TTL:       ttl,
		Value:     value,
		RouteTags: nsRouteCodesToTags(nsRouteCodes),
		RR:        rr,
	}, nil
}

// nsRouteCodesToTags converts GoEdge nsRouteCodes (["province:上海","isp:电信"])
// to the internal route_tags format ("province=上海;isp=电信").
// Empty/default codes produce an empty string.
func nsRouteCodesToTags(codes []string) string {
	var parts []string
	for _, code := range codes {
		if code == "" || code == "default" {
			continue
		}
		// "province:上海" → "province=上海"
		if idx := strings.IndexByte(code, ':'); idx > 0 {
			parts = append(parts, code[:idx]+"="+code[idx+1:])
		}
	}
	return strings.Join(parts, ";")
}

// routeTagsToNSRoutes converts internal route_tags back to nsRoutes for responses.
func routeTagsToNSRoutes(tags string) []nsRoute {
	if tags == "" {
		return nil
	}
	var routes []nsRoute
	for _, part := range strings.Split(tags, ";") {
		if idx := strings.IndexByte(part, '='); idx > 0 {
			dim := part[:idx]
			val := part[idx+1:]
			routes = append(routes, nsRoute{
				Name: val,
				Code: dim + ":" + val,
			})
		}
	}
	return routes
}

func toNSRecordObj(r *iface.Record) nsRecordObj {
	return nsRecordObj{
		Id:       r.ID,
		Name:     strings.TrimSuffix(r.Name, "."),
		Type:     mdns.TypeToString[r.Type],
		Value:    r.Value,
		TTL:      r.TTL,
		IsOn:     true,
		NSRoutes: routeTagsToNSRoutes(r.RouteTags),
	}
}

func toNSRecordObjs(recs []*iface.Record) []nsRecordObj {
	out := make([]nsRecordObj, len(recs))
	for i, r := range recs {
		out[i] = toNSRecordObj(r)
	}
	return out
}
