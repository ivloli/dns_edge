# ECS 地理路由方案（ip2region + filterByGeo）

**版本**：v2.0  
**日期**：2026-06-27

---

## 1. 背景与目标

当前 dns-edge 的分流逻辑是按权重随机选 IP，无地理感知。客户端不管在哪，都可能被解析到延迟高的节点。

目标：利用 DNS EDNS Client Subnet（RFC 7871）携带的客户端子网，结合 ip2region xdb 数据库，按照运营商 + 地域维度返回最合适的记录。

---

## 2. ECS 数据流

```
客户端（上海电信）
  │  dig @dns-edge.example.com www.foo.com +subnet=1.2.3.0/24
  ▼
dns-edge
  │  从 EDNS0 OPT RR 中提取 client subnet（1.2.3.0/24）
  │  取代表 IP（1.2.3.1）查 xdb → "中国|华东|上海|上海|电信"
  │  在路由字典树中匹配 → 命中"中国/电信/上海" → 返回对应 IP 组
  ▼
加权随机从命中 IP 组中选一条返回
```

当请求不带 ECS 时，退化为现有的纯权重随机逻辑（`clientIP == nil`）。

---

## 3. ip2region xdb

- 二进制文件，约 11 MB
- 记录格式：`|` 分隔，无数据用 `0` 占位，支持两种变体：
  - **4 字段**：`国家|省份|城市|ISP`（旧版/社区版常见）
  - **5 字段**：`国家|区域|省份|城市|ISP`（官方最新版，多一个大区字段）
- 查询方式：`searcher.SearchByStr(ip)` → `"中国|华东|上海|上海|电信"`（5 字段）或 `"中国|上海|上海|电信"`（4 字段）
- 支持三种加载模式：File（最省内存）、VectorIndex（推荐，约 1.5 MB 额外内存，查询 < 1 µs）、MemorySearch（全量加载，最快）

推荐用 **VectorIndex 模式**，在 dns-edge 启动时加载一次，查询无 I/O。

`internal/geo/geo.go` 的 `parseRegion` 同时兼容 4/5 字段格式：5 字段时省份在 index 2、ISP 在 index 4；4 字段时省份在 index 1、ISP 在 index 3。

---

## 4. filterByGeo 扁平聚合设计

### 4.1 核心思路

**不使用字典树**，而是采用 **flat map 聚合**：

1. 遍历所有记录，按 `r.Value`（目标 IP）分组
2. 每个 IP 累积布尔标志：`matchProvince`、`matchISP`、`matchCountry`、`isDefault`
3. 最后按优先级层级筛选：`provinceISP > province > isp > country > default > all`

这样避免了树的递归构建和查询开销，单次查询时间 < 5 µs（纯 map 查找 + 布尔运算）。

### 4.2 数据结构（Go）

```go
type ipEntry struct {
    rec             *iface.Record
    matchProvince   bool
    matchISP        bool
    matchCountry    bool
    matchProvinceISP bool
    isDefault       bool
}

// filterByGeo 内部逻辑
func filterByGeo(allRecords []*iface.Record, province, isp, country string) []*iface.Record {
    ipMap := make(map[string]*ipEntry) // key = r.Value（IP 地址）
    
    for _, r := range allRecords {
        tags := parseRouteTags(r.RouteTags) // "province=上海;isp=电信" → map
        e := ipMap[r.Value]
        if e == nil {
            e = &ipEntry{rec: r}
            ipMap[r.Value] = e
        }
        
        // 累积匹配标志
        if tags["province"] == province { e.matchProvince = true }
        if tags["isp"] == isp { e.matchISP = true }
        if tags["country"] == country { e.matchCountry = true }
        if tags["province"] == province && tags["isp"] == isp { e.matchProvinceISP = true }
        if r.RouteTags == "" { e.isDefault = true }
    }
    
    // 按优先级层级筛选
    return selectByTier(ipMap)
}
```

### 4.3 优先级层级

| 层级 | 条件 | 示例 |
|------|------|------|
| 1. provinceISP | matchProvinceISP = true | 上海 + 电信 |
| 2. province | matchProvince = true | 上海 |
| 3. isp | matchISP = true | 电信 |
| 4. country | matchCountry = true | 中国 |
| 5. default | isDefault = true | `route_tags = ""` |
| 6. all | — | 无任何标签也返回所有记录 |

返回**第一个非空层级**的所有 IP。

### 4.4 路由配置格式

记录的 `route_tags` 字段（存储格式）：

```
province=上海;isp=电信
province=北京
isp=联通
country=中国
```

`route_tags` 为空字符串表示默认路由（全局兜底）。

GoEdge 的 `nsRouteCodes`（传入格式）：

```
["province:上海", "isp:电信"]  → 转换为 "province=上海;isp=电信"
["province:北京"]               → 转换为 "province=北京"
[]                              → 转换为 ""（默认路由）
```

转换函数 `nsRouteCodesToTags` 和 `nsRouteTagsToCodes` 双向互转。

---

## 5. 查询路径（改造后）

```
ServeDNS()
  │
  ├─ 提取 ECS clientIP（EDNS0 OPT RR 中解析）
  │
  ├─ if clientIP != nil
  │    └─ WeightProvider.GetWeights(fqdn, qtype, clientIP)
  │         └─ xdb 查 clientIP → parseRegion → province/isp/country
  │         └─ filterByGeo(allRecords, province, isp, country)
  │              └─ flat map 聚合 → 按 tier 筛选 IP 组
  │         └─ 在命中 IP 组内加权随机选一条返回
  │
  └─ else（clientIP == nil，不带 ECS 的请求）
       └─ 现有 WeightProvider 逻辑（纯权重随机，忽略 route_tags）
```

`WeightProvider.GetWeights` 签名：`(fqdn string, qtype uint16, clientIP net.IP) map[string]int`，`clientIP` 为 nil 时退化为纯权重模式。

---

## 6. 权重与地理路由的关系

地理路由和权重不是互斥的：

- **地理路由**：决定从哪个 IP **池**里选（例如"上海电信"对应 3 个 IP）
- **权重**：在选定的 IP 池内做加权随机（例如 3 个 IP 按 30:30:40 分配）

Nacos 权重 DataID 可以按 route tag 分组，格式建议：

```
dns_weights:www.example.com.:A:country=中国;isp=电信
```

值：`{"1.2.3.4":30,"5.6.7.8":70}`

---

## 7. xdb 文件部署

- Dockerfile 中 `COPY ip2region.xdb /etc/dns-edge/`
- Corefile 新增配置项：

```
dns-edge {
    geo {
        xdb  /etc/dns-edge/ip2region.xdb
    }
}
```

`geo` 块缺失或 `xdb` 路径不存在时，禁用地理路由，退化为纯权重。

---

## 8. 开发阶段总结

| 阶段 | 内容 | 状态 |
|------|------|------|
| P1 | `internal/geo/` 包：xdb 封装 + `parseRegion`（兼容 4/5 字段） | ✅ 已完成 |
| P2 | Corefile `geo` 块解析 + xdb 启动加载（VectorIndex 模式） | ✅ 已完成 |
| P3 | `ServeDNS` 集成：ECS clientIP → filterByGeo → 加权随机 | ✅ 已完成 |
| P4 | `Record.RouteTags` 字段 + `nsRouteCodesToTags`/`nsRouteTagsToCodes` 双向转换 | ✅ 已完成 |
| P5 | 单元测试（filterByGeo tier 优先级、ECS 集成、xdb 解析） | ✅ 已完成 |
| P6 | xdb 自动更新（GitHub Releases 定期拉取 + atomic 热替换）；2026-07-07 补充：从零部署（本地无 xdb 文件）时也能自动首次下载，不再永久禁用地理路由 | ✅ 已完成 |
| P7 | NS 模式（智能DNS）接入同一套 `filterByGeo`（2026-07-07 新增，见第 9 节） | ✅ 已完成 |

---

## 9. NS 模式接入 ECS 地理路由（2026-07-07）

**背景**：`filterByGeo`/`pick`（`internal/dns/handler.go`）从设计上就是通用的——只依赖 `iface.Record.RouteTags` 字符串和 `GeoInfo`，不区分记录是 CDN 模式推送来的还是 NS 模式拉取来的。`iface.Record` 也早就有 `RouteTags` 字段、`ZoneStore` 也早就支持同一个 (name,type) 存多条记录——**查询路径（`handler.go`）完全没有改动**，这次纯粹是把 NS 模式拉取记录时"生成 `RouteTags`"这一步补上。

**数据链路**（对比第 2 节的 CDN 模式数据流）：

```
EdgeAdmin「记录管理」勾选线路（省份/ISP，内置线路，多选）
  │  POST routeIds=[1,4,6] → CreateNSRecordRequest.NsRouteIds
  ▼
edgeapi service_ns_record.go
  │  CreateNSRecord/UpdateNSRecord 落库 edgeNSRecords.routeIds（JSON int64 数组）
  │  convertRecordToPB：按 routeIds 反查 edgeNSRoutes，取 code（"province:上海"等），
  │  组装进 pb.NSRecord.NsRoutes（协议里本来就有这个字段，之前从未被赋值）
  ▼
dns-edge internal/edgeagent/agent.go
  │  applyRecord(r *pb.NSRecord)：收集 r.NsRoutes[].Code（非空的）
  │  调 internal/nsroute.CodesToTags(codes) → "province=上海;isp=电信"
  │  赋给 iface.Record.RouteTags
  ▼
internal/dns/handler.go filterByGeo（无需改动，跟 CDN 模式共用同一套代码）
```

**共享转换逻辑**：`nsRouteCodesToTags`（原本只在 `internal/api/edgedns_provider.go` 给 CDN 模式用）抽到了新包 `internal/nsroute`（`CodesToTags`），`internal/api` 和 `internal/edgeagent` 都从这里调用，避免重复实现两遍。

**已知限制**：只支持内置线路（`edgeNSRoutes.code` 前缀 `country:`/`province:`/`isp:`），不支持自定义 IP 段/CIDR/地域 ID 线路——`NSRoute.Ranges` 是一套独立的 JSON 结构（`{type: ipRange|cidr|region, params:{...}}`），要完整支持需要额外解析 CIDR + 反查 `regions` 系列表，复杂度高很多；CDN 模式本身的 `nsRouteCodesToTags` 也只处理 `code`，不解析 `RangesJSON`，所以这次保持两边能力对齐。

**验证**：本机对同一记录名配两条不同线路的记录，`dig +subnet=` 精确匹配、无 ECS 随机、境外 IP 全兜底三种场景全部符合预期，跟 CDN 模式已有的 `handler_test.go` 用例行为一致。
