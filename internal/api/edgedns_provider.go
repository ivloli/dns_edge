package api

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	mdns "github.com/miekg/dns"

	"dns-edge/internal/iface"
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

// recordIDCounter generates in-process record IDs when there is no database.
// Starts at 1; 0 is reserved as "unset".
var recordIDCounter int64

func nextRecordID() int64 {
	return atomic.AddInt64(&recordIDCounter, 1)
}

// zoneID derives a stable int64 zone ID from the apex FQDN using FNV-1a.
// This is deterministic and collision-resistant for reasonable zone counts.
func zoneID(apex string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(apex))
	v := int64(h.Sum64())
	if v < 0 {
		return -v // keep positive
	}
	return v
}

// ── request/response types ────────────────────────────────────────────────────

type getTokenReq struct {
	Type        string `json:"type"`
	AccessKeyID string `json:"accessKeyId"`
	AccessKey   string `json:"accessKey"`
}

type nsRecordObj struct {
	Id       int64     `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Value    string    `json:"value"`
	TTL      uint32    `json:"ttl"`
	IsOn     bool      `json:"isOn"`
	NSRoutes []nsRoute `json:"nsRoutes"`
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

	snap := s.store.Snapshot()
	zones := make([]nsDomainObj, 0, len(snap))
	for apex := range snap {
		zones = append(zones, nsDomainObj{
			Id:   zoneID(apex),
			Name: strings.TrimSuffix(apex, "."),
			IsOn: true,
		})
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

	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsDomains": zones}))
}

func (s *Server) edgeDNSFindDomain(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, "name required"))
		return
	}
	apex := iface.FQDN(req.Name)
	snap := s.store.Snapshot()
	if _, ok := snap[apex]; !ok {
		// Auto-create an empty zone so GoEdge can immediately create records.
		// dns-edge has no persistent state; GoEdge is the source of truth.
		if err := s.store.Update(&iface.Zone{
			Name:    apex,
			Records: make(map[iface.RecordKey][]*iface.Record),
		}); err != nil {
			c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
			return
		}
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsDomain": nsDomainObj{
		Id:   zoneID(apex),
		Name: strings.TrimSuffix(apex, "."),
		IsOn: true,
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

	apex, err := s.resolveApexByID(req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	recs := s.listRecordsInZone(apex)

	if req.Offset >= len(recs) {
		recs = nil
	} else {
		recs = recs[req.Offset:]
		if req.Size < len(recs) {
			recs = recs[:req.Size]
		}
	}

	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecords": toNSRecordObjs(recs, apex)}))
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
	apex, err := s.resolveApexByID(req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}
	recs, err := s.queryByNameType(req.NSDomainID, req.Name, req.Type)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	if len(recs) == 0 {
		c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecord": nil}))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecord": toNSRecordObj(recs[0], apex)}))
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
	apex, err := s.resolveApexByID(req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}
	recs, err := s.queryByNameType(req.NSDomainID, req.Name, req.Type)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecords": toNSRecordObjs(recs, apex)}))
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

	apex, err := s.resolveApexByID(req.NSDomainID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	rec, err := nsToRecord(req.Name, apex, req.Type, req.Value, req.TTL, req.NSRouteCodes)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}
	rec.ID = nextRecordID()

	if err := s.store.PutRecord(apex, rec); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
	c.JSON(http.StatusOK, edgeDNSOK(gin.H{"nsRecordId": rec.ID}))
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

	apex, err := s.findApexForRecord(req.NSRecordID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	rec, err := nsToRecord(req.Name, apex, req.Type, req.Value, req.TTL, req.NSRouteCodes)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(400, err.Error()))
		return
	}
	rec.ID = req.NSRecordID

	if err := s.store.PutRecord(apex, rec); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
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

	apex, err := s.findApexForRecord(req.NSRecordID)
	if err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(404, err.Error()))
		return
	}

	if err := s.store.DropRecord(apex, req.NSRecordID); err != nil {
		c.JSON(http.StatusOK, edgeDNSErrResp(500, err.Error()))
		return
	}
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

// resolveApexByID finds the zone apex whose zoneID() matches id.
func (s *Server) resolveApexByID(id int64) (string, error) {
	for apex := range s.store.Snapshot() {
		if zoneID(apex) == id {
			return apex, nil
		}
	}
	return "", fmt.Errorf("zone %d not found", id)
}

// findApexForRecord scans all zones to find the one containing recordID.
func (s *Server) findApexForRecord(recordID int64) (string, error) {
	for apex, zone := range s.store.Snapshot() {
		for _, recs := range zone.Records {
			for _, r := range recs {
				if r.ID == recordID {
					return apex, nil
				}
			}
		}
	}
	return "", fmt.Errorf("record %d not found", recordID)
}

// listRecordsInZone returns all records in a zone as a flat slice.
func (s *Server) listRecordsInZone(apex string) []*iface.Record {
	snap := s.store.Snapshot()
	zone, ok := snap[apex]
	if !ok {
		return nil
	}
	var out []*iface.Record
	for _, recs := range zone.Records {
		out = append(out, recs...)
	}
	return out
}

// queryByNameType returns records in domainID's zone matching (name, type).
func (s *Server) queryByNameType(domainID int64, name, recType string) ([]*iface.Record, error) {
	apex, err := s.resolveApexByID(domainID)
	if err != nil {
		return nil, err
	}
	qtype, ok := mdns.StringToType[strings.ToUpper(recType)]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", recType)
	}
	fqdn := qualifyName(name, apex)
	recs := s.store.Lookup(fqdn, qtype)
	// Verify the records belong to the requested zone (Lookup searches globally).
	zone := s.store.FindZone(fqdn)
	if zone == nil || zone.Name != apex {
		return nil, nil
	}
	return recs, nil
}

// nsToRecord converts edgeDNSAPI fields to an iface.Record.
// apex is the zone FQDN (e.g. "example.com."); name may be a short label
// ("www"), a relative name ("www.sub"), or already a FQDN ("www.example.com.").
// If name does not already end with apex, apex is appended.
func nsToRecord(name, apex, recType, value string, ttl uint32, nsRouteCodes []string) (*iface.Record, error) {
	qtype, ok := mdns.StringToType[strings.ToUpper(recType)]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", recType)
	}
	fqdn := qualifyName(name, apex)
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

// qualifyName turns a short label or relative name into a FQDN under apex.
// apex must already have a trailing dot.
// Examples: qualifyName("www", "example.com.") → "www.example.com."
//
//	qualifyName("www.example.com.", "example.com.") → "www.example.com."
//	qualifyName("example.com.", "example.com.")   → "example.com."
func qualifyName(name, apex string) string {
	// Already a FQDN that ends with apex — leave it.
	if strings.HasSuffix(name, ".") {
		return name
	}
	// Relative name: append ".<apex>" (apex already has trailing dot).
	return name + "." + apex
}

// nsRouteCodesToTags converts GoEdge nsRouteCodes (["province:上海","isp:电信"])
// to the internal route_tags format ("province=上海;isp=电信").
func nsRouteCodesToTags(codes []string) string {
	var parts []string
	for _, code := range codes {
		if code == "" || code == "default" {
			continue
		}
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

// toNSRecordObj converts a Record to the edgeDNSAPI wire format.
// apex is the zone FQDN (e.g. "example.com."); the short relative name
// (e.g. "www") is derived by stripping ".<apex>" from the FQDN.
func toNSRecordObj(r *iface.Record, apex string) nsRecordObj {
	shortName := r.Name
	// Strip the ".<apex>" suffix to get back the label GoEdge sent.
	if strings.HasSuffix(shortName, "."+apex) {
		shortName = shortName[:len(shortName)-len("."+apex)]
	} else {
		// Fallback: just strip trailing dot.
		shortName = strings.TrimSuffix(shortName, ".")
	}
	return nsRecordObj{
		Id:       r.ID,
		Name:     shortName,
		Type:     mdns.TypeToString[r.Type],
		Value:    r.Value,
		TTL:      r.TTL,
		IsOn:     true,
		NSRoutes: routeTagsToNSRoutes(r.RouteTags),
	}
}

func toNSRecordObjs(recs []*iface.Record, apex string) []nsRecordObj {
	out := make([]nsRecordObj, len(recs))
	for i, r := range recs {
		out[i] = toNSRecordObj(r, apex)
	}
	return out
}
