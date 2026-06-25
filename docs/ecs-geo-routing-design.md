# ECS 地理路由方案（ip2region + 字典树）

**版本**：v1.0  
**日期**：2026-06-25

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
- 每条记录格式：`国家|区域|省份|城市|ISP`（`|` 分隔，无数据用 `0` 占位）
- 查询方式：`searcher.SearchByStr(ip)` → `"中国|华东|上海|上海|电信"`
- 支持三种加载模式：File（最省内存）、VectorIndex（推荐，约 1.5 MB 额外内存，查询 < 1 µs）、MemorySearch（全量加载，最快）

推荐用 **VectorIndex 模式**，在 dns-edge 启动时加载一次，查询无 I/O。

---

## 4. 路由字典树设计

### 4.1 树结构

节点层级（从粗到细）：

```
root
 └─ 国家（Country）
     └─ ISP（运营商）       ← 最核心的分流维度
         └─ 省份（Province）
             └─ 城市（City）  ← 可选，按需启用
```

每个节点存储：
- `children map[string]*RouteNode`
- `records []*iface.Record` — 该节点命中时返回的 IP 组（nil 表示继续向上 fallback）

### 4.2 Fallback 规则

匹配从最细粒度开始，逐级向上退：

```
city → province → ISP → country → default（全局兜底）
```

例：客户端是上海电信，但字典树只配置到省份级别 → 命中"中国/电信/上海"节点失败 → 退回"中国/电信" → 命中返回电信 IP 组。

### 4.3 数据结构（Go）

```go
type RouteNode struct {
    children map[string]*RouteNode
    records  []*iface.Record // nil = no override at this level
}

type GeoRouter struct {
    root    *RouteNode
    searcher *ip2region.Searcher // xdb VectorIndex
}

// Query 返回 clientIP 对应的 IP 组；未命中任何节点时返回 nil（调用方用默认权重）
func (g *GeoRouter) Query(fqdn string, qtype uint16, clientIP net.IP) []*iface.Record
```

### 4.4 路由配置格式

在 PG 的 `routes` 表（新增）或记录的 `route_tags` JSON 字段中配置，格式建议：

```
country=中国;isp=电信;province=上海
country=中国;isp=联通
country=中国
default
```

GoEdge 调 `GetRoutes` 时，dns-edge 从字典树的所有叶节点反向生成 `{name, code}` 列表返回。

---

## 5. 查询路径（改造后）

```
ServeDNS()
  │
  ├─ 提取 ECS clientIP（已有，Phase 6 EDNS0 已实现）
  │
  ├─ if clientIP != nil && GeoRouter != nil
  │    └─ geoRouter.Query(name, qtype, clientIP)
  │         └─ xdb 查 IP → 解析 country/isp/province/city
  │         └─ 字典树从细到粗匹配
  │         └─ 找到 records → 加权随机返回
  │
  └─ else
       └─ 现有 WeightProvider 逻辑（纯权重随机）
```

GeoRouter 实现 `WeightProvider` 接口的扩展版，或作为独立的 `GeoWeightProvider`，在 `CompositeWeightProvider` 中优先级最高。

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

## 8. 开发阶段规划

| 阶段 | 内容 | 预估 |
|------|------|------|
| P1 | `internal/geo/` 包：xdb 封装 + `GeoRouter` 字典树实现 | 1.5 天 |
| P2 | Corefile `geo` 块解析 + xdb 启动加载 | 0.5 天 |
| P3 | `ServeDNS` 集成：ECS clientIP → GeoRouter → 加权随机 | 0.5 天 |
| P4 | PG `routes` 表 or 记录 `route_tags` 字段（存地理路由规则） | 0.5 天 |
| P5 | 单元测试（字典树 fallback 逻辑、ECS 集成） | 1 天 |

总计约 **4 天**。

---

## 9. 待确认事项

1. **路由规则存在哪**：记录级别的 `route_tags` 字段（灵活但复杂），还是单独一张 `routes` 表（清晰但多一次查询）？
2. **城市粒度**：是否需要到城市级别？ip2region 的城市数据覆盖率参差不齐，省份级别通常已够用。
3. **xdb 版本更新**：ip2region 数据会定期更新，是否需要热更新机制（运行时替换 xdb），还是重启节点即可？
4. **多国支持**：目前设计以中国运营商为主，国际节点是否只需要按国家路由？
5. **GoEdge GetRoutes 接口**：地理路由启用后，`GetRoutes` 返回的线路列表需要包含所有配置的 route tag，GoEdge 管理员才能在控制台选线路。需要确认 GoEdge 如何展示和使用这些线路。
