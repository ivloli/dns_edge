# dns-edge GoEdge Provider 接入方案

**版本**：v3.0  
**日期**：2026-06-25  
**分支**：`feature/goedge-provider`

---

## 1. 背景与目标

### 1.1 GoEdge 的 DNS 体系

GoEdge 是一套自托管 CDN 管理平台，由三个独立进程组成：

```
EdgeAdmin（Web 管理后台）
    │ gRPC / REST
    ▼
EdgeAPI（中心管理服务，有自己的 MySQL）
    │ gRPC
    ▼
EdgeNode（边缘节点，做反向代理/缓存/WAF）
```

GoEdge 同时支持 **EdgeDNS** 角色，与 EdgeNode 平级，专门负责边缘 DNS 解析：

```
EdgeAdmin
    │
    ▼
EdgeAPI ─── DNS Provider 接口 ───► 外部 DNS 后端（阿里云 / Cloudflare / dns-edge …）
    │
    ▼
EdgeDNS（边缘 DNS 节点，和 EdgeNode 地位相同）
```

### 1.2 dns-edge 的角色

dns-edge 既可以作为 GoEdge 的 **DNS 提供商插件**（customHTTP 模式），也可以作为一个原生 **EdgeDNS 节点**（edgeDNSAPI 模式）被 GoEdge 管理。两套接口可并存，功能上完全等价，差别在于 edgeDNSAPI 模式支持更丰富的地理路由线路配置（省份/ISP/国家）。

### 1.3 目标

1. **已完成**：实现 GoEdge customHTTP Provider 接口（`POST /goedge/dns`）
2. **本文档新增**：实现 GoEdge edgeDNSAPI 兼容接口（`POST /NS*Service/*`），支持智能 DNS 地理分流配置
3. **本文档新增**：在公司 edgeapi repo 里实现 NS 商业版服务端，让 GoEdge 管理后台具备 EdgeDNS 集群管理能力

---

## 2. 架构说明

### 2.1 存储与同步架构

中心存储**只有 edgeapi MySQL**（GoEdge 本身就依赖 MySQL，这是固有要求）。dns-edge 节点不直连任何数据库，记录全部存内存，通过两种方式从 edgeapi 同步：

```
GoEdge EdgeAdmin
    │
    ▼
GoEdge EdgeAPI（edgeapi，MySQL）
    │
    ├─ 推送（低延迟，变更即通知）
    │    edgeapi → dns-edge /internal/sync
    │
    └─ 轮询（兜底 + 节点扩容）
         dns-edge 定期拉取 /NSRecordService/ListNSRecords
         │
         ▼
┌─────────────────────────────────────────────┐
│  dns-edge 节点（每个边缘节点）                │
│                                             │
│  ZoneStore（内存） ◄── 推送 / 轮询同步        │
│       │                                     │
│  [DNS Handler]                              │
│  [内部 REST API] /api/v1/...                │
└─────────────────────────────────────────────┘
```

### 2.2 目标架构（含两套 GoEdge 接口）

```
GoEdge EdgeAPI
  │
  ├─ customHTTP Provider（已完成）
  │    POST /goedge/dns  action=AddRecord/UpdateRecord/…
  │
  └─ edgeDNSAPI Provider（待实现）
       POST /APIAccessTokenService/getAPIAccessToken
       POST /NSDomainService/…
       POST /NSRecordService/…
       POST /NSRouteService/…
       │
       ▼
┌──────────────────────────────────────────────────────────┐
│  dns-edge                                                │
│                                                          │
│  ZoneStore（内存） ◄── 推送 / 轮询（来自 edgeapi）         │
│       │                                                  │
│  [DNS Handler]  [customHTTP API]  [edgeDNSAPI]           │
│                  /goedge/dns       /NS*Service/*          │
│                       │                 │                │
│                    写内存             写内存              │
└──────────────────────────────────────────────────────────┘
```

---

## 3. 接口方案对比

GoEdge 支持两种接入方式，均实现同一套 `ProviderInterface`，功能等价：

| | customHTTP（已完成） | edgeDNSAPI（待实现） |
|---|---|---|
| HTTP 接口数量 | **1 个**端点，`action` 字段区分 | **14 个**独立路径 |
| 鉴权方式 | `SHA1(secret@timestamp)` 签名 | AccessKey 换 Bearer Token |
| 地理路由线路 | `GetRoutes` 只返回 `default` | `NSRouteService` 返回省份/ISP/国家完整列表 |
| 智能 DNS 分流 | 不支持（GoEdge 拿不到线路列表） | **支持**（GoEdge 调 `FindAllDefaultChinaProvinceRoutes` 等） |
| 实现难度 | 低（已完成） | 中 |

---

## 4. 已完成：customHTTP 接口（Phase 11）

### 4.1 鉴权

```
Timestamp: <unix_timestamp>
Token: <sha1(secret + "@" + timestamp)>
```

### 4.2 端点

`POST /goedge/dns`，Body 中 `action` 字段区分操作：

| action | 说明 |
|--------|------|
| `GetDomains` | 返回所有 zone 名称列表 |
| `GetRecords` | 返回指定 zone 的所有记录 |
| `GetRoutes` | 返回路线列表（当前仅返回 `default`） |
| `QueryRecord` | 查单条记录 |
| `QueryRecords` | 查多条记录 |
| `AddRecord` | 新建记录 |
| `UpdateRecord` | 更新记录 |
| `DeleteRecord` | 删除记录 |
| `DefaultRoute` | 返回默认线路 code `"default"` |

### 4.3 配置

```
api {
    listen        :28082
    goedge_secret <your-shared-secret>
}
```

GoEdge 侧：Provider 类型选 `customHTTP`，填写 `url` 和 `secret`。

### 4.4 实现状态

| 模块 | 状态 |
|------|------|
| `internal/api/goedge_provider.go` | ✅ 已完成 |
| 鉴权（SHA1） | ✅ 已完成 |
| `goedge_secret` 配置解析 | ✅ 已完成 |
| 单元测试（20 个用例） | ✅ 已完成 |
| 与 GoEdge 联调 | ⬜ 待做 |

---

## 5. 待实现：edgeDNSAPI 服务端（Phase 14）

### 5.1 概述

GoEdge edgeDNSAPI 是 GoEdge 调用另一个 GoEdge 实例（或兼容服务）的 HTTP JSON-RPC 接口。dns-edge 实现这套接口后，GoEdge 可以把 dns-edge 当作原生 EdgeDNS 节点来管理，并通过 `NSRouteService` 配置地理路由线路，实现智能 DNS 分流。

接口调用方式：`POST /<ServiceName>/<MethodName>`，Content-Type: application/json，响应格式统一为：

```json
{ "code": 200, "message": "", "data": { ... } }
```

### 5.2 鉴权流程

GoEdge 先调 `getAPIAccessToken` 换取 Bearer Token，后续所有请求带 `X-Edge-Access-Token` header：

```
POST /APIAccessTokenService/getAPIAccessToken
请求：{ "type": "user", "accessKeyId": "xxx", "accessKey": "xxx" }
响应：{ "code": 200, "data": { "token": "...", "expiresAt": 1234567890 } }
```

Token 有效期内缓存，GoEdge 侧会在过期前 600 秒刷新。

### 5.3 需实现的接口

#### NSDomainService

| 路径 | 请求参数 | 响应 | 说明 |
|------|---------|------|------|
| `/NSDomainService/ListNSDomains` | `offset` int, `size` int | `{ nsDomains: [{id,name,isOn,isDeleted}] }` | 分页列出所有 zone |
| `/NSDomainService/FindNSDomainWithName` | `name` string | `{ nsDomain: {id,name} }` | 按名称查 zone |

#### NSRecordService

| 路径 | 请求参数 | 响应 | 说明 |
|------|---------|------|------|
| `/NSRecordService/ListNSRecords` | `nsDomainId` int, `offset` int, `size` int | `{ nsRecords: [{id,name,type,value,ttl,nsRoutes}] }` | 分页列出记录 |
| `/NSRecordService/FindNSRecordWithNameAndType` | `nsDomainId` int, `name` string, `type` string | `{ nsRecord: {...} }` | 查单条记录 |
| `/NSRecordService/FindNSRecordsWithNameAndType` | `nsDomainId` int, `name` string, `type` string | `{ nsRecords: [...] }` | 查多条记录 |
| `/NSRecordService/CreateNSRecord` | `nsDomainId` int, `name` string, `type` string, `value` string, `ttl` int, `nsRouteCodes` []string | `{ nsRecordId: int }` | 创建记录，`nsRouteCodes` 写入 `route_tags` |
| `/NSRecordService/UpdateNSRecord` | `nsRecordId` int, `name` string, `type` string, `value` string, `ttl` int, `nsRouteCodes` []string, `isOn` bool | — | 更新记录 |
| `/NSRecordService/DeleteNSRecord` | `nsRecordId` int | — | 删除记录（软删除） |

Record 对象中 `nsRoutes` 字段：

```json
"nsRoutes": [
  { "name": "中国-上海", "code": "province:上海" }
]
```

#### NSRouteService（地理路由线路）

| 路径 | 说明 | 返回示例 |
|------|------|---------|
| `/NSRouteService/FindAllDefaultWorldRegionRoutes` | 世界区域线路 | `[{name:"中国",code:"country:中国"}, {name:"美国",code:"country:美国"}]` |
| `/NSRouteService/FindAllDefaultChinaProvinceRoutes` | 中国省份线路 | `[{name:"上海",code:"province:上海"}, {name:"北京",code:"province:北京"}]` |
| `/NSRouteService/FindAllDefaultISPRoutes` | 运营商线路 | `[{name:"电信",code:"isp:电信"}, {name:"联通",code:"isp:联通"}, {name:"移动",code:"isp:移动"}]` |
| `/NSRouteService/FindAllAgentNSRoutes` | 代理线路（暂不支持，返回空列表） | `[]` |
| `/NSRouteService/FindAllNSRoutes` | 自定义线路（暂不支持，返回空列表） | `[]` |

### 5.4 route_tags 与 nsRouteCodes 的映射

`nsRouteCodes` 是 GoEdge 传入的线路 code 列表（数组，通常只有一个元素），对应我们 `route_tags` 字段的格式：

| nsRouteCodes | route_tags 存储 |
|---|---|
| `[]` 或 `["default"]` | `""` （默认路由） |
| `["province:上海"]` | `"province=上海"` |
| `["isp:电信"]` | `"isp=电信"` |
| `["country:中国"]` | `"country=中国"` |
| `["province:上海", "isp:电信"]` | `"province=上海;isp=电信"` |

转换规则：`code` 格式为 `<维度>:<值>`，存储时转为 `<维度>=<值>`，多个用 `;` 分隔。

### 5.5 新增文件

- `internal/api/edgedns_provider.go`：所有 NS*Service 端点的 handler
- `internal/api/edgedns_auth.go`：AccessKey 管理、Token 生成与校验
- 配置新增 `edgedns_access_key_id` 和 `edgedns_access_key_secret`

### 5.6 配置

```
api {
    listen                  :28082
    goedge_secret           <customHTTP-secret>
    edgedns_access_key_id   <access-key-id>
    edgedns_access_key_secret <access-key-secret>
}
```

GoEdge 侧：Provider 类型选 `edgeDNSAPI`，填写 `host`、`accessKeyId`、`accessKeySecret`。

### 5.7 实现状态

| 模块 | 状态 |
|------|------|
| `edgedns_provider.go` NS*Service 端点 | ⬜ 待做 |
| `edgedns_auth.go` AccessKey/Token 鉴权 | ⬜ 待做 |
| NSRouteService 地理路由线路 | ⬜ 待做 |
| nsRouteCodes ↔ route_tags 双向转换 | ⬜ 待做 |
| 配置层 `edgedns_access_key_*` | ⬜ 待做 |
| 单元测试 | ⬜ 待做 |
| 与 GoEdge 联调 | ⬜ 待做 |

---

## 6. 待实现：edgeapi NS 商业版服务端（Phase 15）

### 6.1 概述

公司 edgeapi repo 是 GoEdge 开源版 fork，NS 相关服务（`NSRecordService`、`NSRouteService`、`NSDomainService`）均被 `//go:build !plus` 门控，实际为空 stub。本阶段目标是在 edgeapi 里补全这套服务端实现，让 GoEdge EdgeAdmin 管理后台具备 EdgeDNS 集群管理能力——在 EdgeAdmin 里创建 NS 集群、添加 dns-edge 节点、配置地理路由线路，GoEdge 通过 edgeDNSAPI 下发记录到 dns-edge。

### 6.2 需要改动的文件

#### edgeapi repo

| 文件 | 改动内容 |
|------|---------|
| `internal/nodes/api_node_services_hook.go` | 去掉 `!plus` 门控，注册 `NSRecordService`、`NSRouteService`、`NSDomainService`、`NSClusterService`、`NSNodeService` |
| `internal/db/models/nameservers/ns_record_dao.go` | 补全 `CreateNSRecord`、`ListNSRecords`、`FindNSRecordWithNameAndType`、`FindNSRecordsWithNameAndType`、`UpdateNSRecord`、`DeleteNSRecord` |
| `internal/db/models/nameservers/ns_route_dao.go` | 补全 `FindAllDefaultWorldRegionRoutes`、`FindAllDefaultChinaProvinceRoutes`、`FindAllDefaultISPRoutes`、`FindAllNSRoutes` |
| `internal/db/models/nameservers/ns_domain_dao.go` | 补全 `ListNSDomains`、`FindNSDomainWithName`、`CreateNSDomain` |
| `internal/rpc/services/service_ns_record.go` | 新建，实现 `NSRecordService` gRPC + REST handler |
| `internal/rpc/services/service_ns_route.go` | 新建，实现 `NSRouteService` gRPC + REST handler |
| `internal/rpc/services/service_ns_domain.go` | 新建，实现 `NSDomainService` gRPC + REST handler |
| `internal/nodes/rest_server.go` | `restServicesMap` 增加 NS*Service 注册 |

#### edgeadmin repo（前端，后续阶段）

EdgeAdmin 前端 NS 集群管理界面（添加集群、节点、查看记录）——需要单独评估工作量。

### 6.3 数据库表（MySQL，edgeapi 现有表结构）

- `edgeNSRecords`：记录表，有 `name`、`type`、`value`、`ttl`、`nsDomainId`、`nsRouteIds` 字段
- `edgeNSRoutes`：路由线路表
- `edgeNSDomains`：域名（zone）表
- `edgeNSClusters`、`edgeNSNodes`：集群/节点管理

### 6.4 与 dns-edge 的关系

```
GoEdge EdgeAdmin
    │ 管理 NS 集群和节点
    ▼
GoEdge EdgeAPI（edgeapi，Phase 15 补全，MySQL 存储）
    │ 通过 edgeDNSAPI 下发记录 + 推送变更通知
    ▼
dns-edge（Phase 14 补全 edgeDNSAPI 服务端）
    │ 写内存 + 推送到其他节点
    ▼
全球各 dns-edge 节点（推送 + 轮询同步，无 PG）
```

### 6.5 实现状态

| 模块 | 状态 |
|------|------|
| `api_node_services_hook.go` 去除 `!plus` 门控 | ⬜ 待做 |
| `ns_record_dao.go` 补全 CRUD | ⬜ 待做 |
| `ns_route_dao.go` 补全路由查询 | ⬜ 待做 |
| `ns_domain_dao.go` 补全域名查询 | ⬜ 待做 |
| `service_ns_record.go` / `_route` / `_domain` | ⬜ 待做 |
| REST handler 注册 | ⬜ 待做 |
| 单元测试 | ⬜ 待做 |
| EdgeAdmin 前端 | ⬜ 待评估 |

---

## 7. 开发进度总览

| 阶段 | 内容 | 状态 |
|------|------|------|
| P1 | customHTTP 单端点（全部 action） | ✅ 已完成 |
| P2 | customHTTP 鉴权（SHA1） | ✅ 已完成 |
| P3 | Corefile `goedge_secret` 配置 | ✅ 已完成 |
| P4 | customHTTP 单元测试（20 用例） | ✅ 已完成 |
| P5 | customHTTP 与 GoEdge 联调 | ⬜ 待做 |
| P14 | dns-edge edgeDNSAPI 服务端 + 地理路由线路 | ⬜ 待做 |
| P15 | edgeapi NS 商业版服务端（NSRecord/Route/Domain Service） | ⬜ 待做 |
| P16 | EdgeAdmin 前端 NS 集群管理界面 | ⬜ 待评估 |

---

## 8. 待确认事项

1. **Phase 14 优先级**：先做 dns-edge 的 edgeDNSAPI 服务端（P14），还是先完成 customHTTP 联调（P5）？
2. **路由线路 code 格式**：`province:上海` / `isp:电信` / `country:中国` 这个格式是否与 ECS 地理路由（Phase 13）的 `route_tags` 格式对齐，还是需要适配层？
3. **edgeapi 存储**：GoEdge 本身就依赖 MySQL，edgeapi MySQL 即为唯一管理面存储，无需迁移 PG。edgeapi 只做管理面，写操作通过 edgeDNSAPI 下发到 dns-edge，dns-edge 存内存，节点间通过推送+轮询同步。✅ 已确认
4. **EdgeAdmin 前端**：是复用现有 GoEdge 商业版前端界面的 HTML/JS（如果有），还是重新开发？
