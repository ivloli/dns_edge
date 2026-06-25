# dns-edge GoEdge Provider 改造方案

**版本**：v1.0  
**日期**：2026-06-24  
**分支**：`feature/goedge-provider`

---

## 1. 背景与目标

### 1.1 背景

dns-edge 当前架构直连 PostgreSQL 和 Nacos，适合中心化单集群部署。公司计划将 dns-edge 作为 GoEdge CDN 平台的 DNS 提供商之一，部署在全球各边缘节点。边缘节点无法直连中心 DB 和 Nacos，所有数据获取和配置管理必须通过 GoEdge EdgeAPI（中心 API 服务）进行。

### 1.2 目标

1. **实现 GoEdge DNS Provider 接口**：dns-edge 暴露一套符合 GoEdge `EdgeDNSAPIProvider` 规范的 HTTP API，使 GoEdge 能将 dns-edge 作为 DNS 提供商管理域名和记录。
2. **去除直连依赖**：移除 dns-edge 对 PostgreSQL 和 Nacos 的直连依赖，改为通过 EdgeAPI 获取数据。
3. **适配边缘部署**：dns-edge 作为纯边缘服务运行，启动时从 EdgeAPI 拉取全量数据，运行时通过长轮询或推送保持同步。

---

## 2. GoEdge Provider 接口规范

GoEdge 通过 `ProviderInterface` 操作 DNS 提供商，具体实现为 `EdgeDNSAPIProvider`（见 `TeaOSLab/EdgeAPI/internal/dnsclients/provider_edge_dns_api.go`）。

### 2.1 鉴权机制

所有 API 请求在 Header 中携带 Access Token：

```
X-Edge-Access-Token: <token>
```

Token 通过独立接口获取，有效期内复用，到期前 600 秒刷新。

**获取 Token：**

```
POST /APIAccessTokenService/getAPIAccessToken
```

请求体：

```json
{
  "type": "user",
  "accessKeyId": "<key_id>",
  "accessKey": "<key_secret>"
}
```

响应体：

```json
{
  "code": 200,
  "message": "",
  "data": {
    "token": "<access_token>",
    "expiresAt": 1751000000
  }
}
```

### 2.2 统一响应格式

所有接口统一响应结构：

```json
{
  "code": 200,
  "message": "",
  "data": { ... }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `code` | int | 200 = 成功，其他值为错误 |
| `message` | string | 错误时的描述信息 |
| `data` | object | 业务数据载体 |

### 2.3 需实现的 HTTP 接口

所有接口均为 `POST`，Content-Type: application/json，通过 `X-Edge-Access-Token` 鉴权。

#### 域名（Zone）接口

| 接口路径 | 请求参数 | 响应 data 字段 | 说明 |
|----------|----------|----------------|------|
| `POST /NSDomainService/ListNSDomains` | `offset` int, `size` int | `nsDomains[]`（id, name, isOn, isDeleted） | 分页列出所有 zone |
| `POST /NSDomainService/FindNSDomainWithName` | `name` string | `nsDomain`（id, name） | 按名称查找 zone，不存在时 data.nsDomain.id = 0 |

#### 记录（Record）接口

| 接口路径 | 请求参数 | 响应 data 字段 | 说明 |
|----------|----------|----------------|------|
| `POST /NSRecordService/ListNSRecords` | `nsDomainId` int64, `offset` int, `size` int | `nsRecords[]`（id, name, type, value, ttl, nsRoutes） | 按 zone ID 分页列出记录 |
| `POST /NSRecordService/FindNSRecordWithNameAndType` | `nsDomainId` int64, `name` string, `type` string | `nsRecord`（同上） | 查询单条记录，不存在时 id = 0 |
| `POST /NSRecordService/FindNSRecordsWithNameAndType` | `nsDomainId` int64, `name` string, `type` string | `nsRecords[]` | 查询多条记录（同名同类型多值场景） |
| `POST /NSRecordService/CreateNSRecord` | `nsDomainId` int64, `name` string, `type` string, `value` string, `ttl` int32, `nsRouteCodes` []string | `nsRecordId` int64 | 创建记录 |
| `POST /NSRecordService/UpdateNSRecord` | `nsRecordId` int64, `name` string, `type` string, `value` string, `ttl` int32, `nsRouteCodes` []string, `isOn` bool | —（code 200） | 更新记录 |
| `POST /NSRecordService/DeleteNSRecord` | `nsRecordId` int64 | —（code 200） | 删除记录 |

#### 线路（Route）接口

GoEdge 用线路（Route）实现 GeoDNS / 分运营商解析。dns-edge 当前不支持 GeoDNS，暂时返回默认线路（`default`），其他线路接口返回空列表。

| 接口路径 | 请求参数 | 响应 data 字段 | 说明 |
|----------|----------|----------------|------|
| `POST /NSRouteService/FindAllDefaultWorldRegionRoutes` | — | `nsRoutes[]`（name, code） | 世界区域线路（暂返回空） |
| `POST /NSRouteService/FindAllDefaultChinaProvinceRoutes` | — | `nsRoutes[]` | 中国省份线路（暂返回空） |
| `POST /NSRouteService/FindAllDefaultISPRoutes` | — | `nsRoutes[]` | ISP 线路（暂返回空） |
| `POST /NSRouteService/FindAllAgentNSRoutes` | — | `nsRoutes[]` | Agent 线路（暂返回空，允许 404） |
| `POST /NSRouteService/FindAllNSRoutes` | — | `nsRoutes[]` | 自定义线路（暂返回空） |

> GoEdge 调用 `FindAllAgentNSRoutes` 时会忽略 404 错误（注释写明"忽略错误，因为老版本的EdgeDNS没有提供这个接口"），可以返回 404 或空列表。

---

## 3. 架构改造方案

### 3.1 当前架构

```
┌─────────────────────────────────────────────────────────┐
│  dns-edge                                               │
│                                                         │
│  [DNS Handler] ─── ZoneStore（内存） ─── [Syncer]       │
│       │                                      │          │
│  [HTTP API]                           PostgreSQL        │
│                                              │          │
│                                       Nacos（权重）      │
└─────────────────────────────────────────────────────────┘
```

### 3.2 目标架构

```
                    ┌──────────────────────────┐
                    │  GoEdge EdgeAPI（中心）   │
                    │  - 域名/记录管理          │
                    │  - 权重配置              │
                    │  - AccessToken 颁发       │
                    └────────────┬─────────────┘
                                 │ HTTP (EdgeAPI 协议)
          ┌──────────────────────┼──────────────────────┐
          ▼                      ▼                      ▼
┌─────────────────┐   ┌─────────────────┐   ┌─────────────────┐
│  dns-edge 节点  │   │  dns-edge 节点  │   │  dns-edge 节点  │
│  (边缘 A)       │   │  (边缘 B)       │   │  (边缘 C)       │
│                 │   │                 │   │                 │
│ DNS Handler     │   │ DNS Handler     │   │ DNS Handler     │
│ ZoneStore(内存) │   │ ZoneStore(内存) │   │ ZoneStore(内存) │
│ GoEdge API层    │   │ GoEdge API层    │   │ GoEdge API层    │
│ EdgeAPI Syncer  │   │ EdgeAPI Syncer  │   │ EdgeAPI Syncer  │
└─────────────────┘   └─────────────────┘   └─────────────────┘
```

### 3.3 核心变化

| 组件 | 现在 | 改造后 |
|------|------|--------|
| 数据来源 | PostgreSQL 直连 | EdgeAPI HTTP 拉取 |
| 权重配置 | Nacos ListenConfig | EdgeAPI 权重接口（或 NSRoute） |
| 记录写入 | PG INSERT/UPDATE | EdgeAPI CreateNSRecord / UpdateNSRecord |
| 启动全量加载 | `SELECT * FROM records` | `ListNSDomains` + `ListNSRecords` 分页 |
| 增量同步 | PG `WHERE updated_at > ?` | EdgeAPI 长轮询 / 定时全量 |
| 配置文件 | `postgres { dsn ... }` + `nacos { ... }` | `edgeapi { host ... accessKeyId ... }` |

---

## 4. 新配置块设计

移除 `postgres` 和 `nacos` 块，新增 `edgeapi` 块：

```
dns-edge {
    listen   :15300
    workers  0
    tcp      true

    api {
        listen :28082
    }

    edgeapi {
        host           https://edge-api.example.com
        access_key_id  <key_id>
        access_key     <key_secret>
        role           user          # user | admin，默认 user
        sync_interval  30s           # 全量同步间隔，默认 30s
        timeout        10s           # 单次请求超时，默认 10s
    }

    sync {
        interval  30s
        prob      0.01
        ratelimit 100
    }
}
```

---

## 5. 数据流设计

### 5.1 启动流程

```
启动
  │
  ├─ 1. 解析配置（edgeapi block）
  ├─ 2. EdgeAPI Client 初始化，获取 AccessToken
  ├─ 3. ListNSDomains 分页拉取全部 Zone
  ├─ 4. 对每个 Zone：ListNSRecords 分页拉取全部 Record
  ├─ 5. 构建 ZoneStore（内存）
  ├─ 6. 启动 DNS Handler
  ├─ 7. 启动 GoEdge Provider HTTP API（/NSRecordService/... 等）
  └─ 8. 启动 EdgeAPI Syncer（定时全量同步）
```

### 5.2 同步策略

由于 EdgeAPI 没有提供 `updated_at` 过滤的增量接口（不像直连 PG 可以按 `updated_at` 查增量），改为以下策略：

| 场景 | 策略 |
|------|------|
| 正常运行 | 每 30s 全量拉取一次（所有 Zone + Record），替换内存 |
| GoEdge 写入 | GoEdge 通过 Provider API 写入后，dns-edge 直接更新内存（写路径双写） |
| 启动 | 全量拉取，拉取完成前 DNS 返回 SERVFAIL 或使用旧数据（可配置） |

### 5.3 写路径（GoEdge → dns-edge）

```
GoEdge EdgeAdmin
  │
  │ POST /NSRecordService/CreateNSRecord
  ▼
dns-edge GoEdge API 层
  │
  ├─ 1. 转换为内部 Record 格式
  ├─ 2. 调用 EdgeAPI CreateNSRecord（写到中心）
  ├─ 3. 更新本实例 ZoneStore（内存热更新）
  └─ 4. 返回 { code: 200, data: { nsRecordId: ... } }
```

> 注意：dns-edge 自身不再是数据 source of truth，EdgeAPI 才是。本实例的写操作会先透传到 EdgeAPI，再更新本地内存。

---

## 6. 新增模块规划

### 6.1 `internal/edgeapi/` — EdgeAPI 客户端

负责与 GoEdge EdgeAPI 通信：

```
internal/edgeapi/
  client.go          # HTTP 客户端，Token 管理，doAPI()
  types.go           # 请求/响应类型（对应 edgeapi 包的 response_*.go）
  zone.go            # ListNSDomains, FindNSDomainWithName
  record.go          # ListNSRecords, FindNSRecord*, CreateNSRecord, UpdateNSRecord, DeleteNSRecord
  route.go           # FindAllXxxRoutes
```

### 6.2 `internal/api/goedge.go` — GoEdge Provider API

在现有 Gin 服务中新增路由组，暴露 GoEdge 期望的 HTTP 接口：

```
/APIAccessTokenService/getAPIAccessToken    POST
/NSDomainService/ListNSDomains              POST
/NSDomainService/FindNSDomainWithName       POST
/NSRecordService/ListNSRecords              POST
/NSRecordService/FindNSRecordWithNameAndType POST
/NSRecordService/FindNSRecordsWithNameAndType POST
/NSRecordService/CreateNSRecord             POST
/NSRecordService/UpdateNSRecord             POST
/NSRecordService/DeleteNSRecord             POST
/NSRouteService/FindAllDefaultWorldRegionRoutes POST
/NSRouteService/FindAllDefaultChinaProvinceRoutes POST
/NSRouteService/FindAllDefaultISPRoutes     POST
/NSRouteService/FindAllAgentNSRoutes        POST
/NSRouteService/FindAllNSRoutes             POST
```

### 6.3 `internal/syncer/edgeapi_syncer.go` — 全量同步器

替换现有 PG Syncer，改为通过 EdgeAPI 全量拉取：

```go
type EdgeAPISyncer struct {
    client   *edgeapi.Client
    store    iface.ZoneStore
    interval time.Duration
    log      *zap.Logger
}
```

### 6.4 配置层变化

- 移除 `PostgresConfig`、`NacosConfig`
- 新增 `EdgeAPIConfig`
- `Corefile` parser 新增 `edgeapi` 块解析

---

## 7. 兼容性与迁移

### 7.1 接口兼容

现有的 `GET/POST /api/v1/domains` 和 `GET/POST /api/v1/domains/:domain/records` 等内部 REST API **继续保留**，用于本地测试和调试。

GoEdge Provider API（`/NSDomainService/...` 等）作为新路由组挂载，不影响现有接口。

### 7.2 配置迁移

| 旧配置 | 新配置 | 说明 |
|--------|--------|------|
| `postgres { dsn ... }` | `edgeapi { host ... }` | 数据来源改为 EdgeAPI |
| `nacos { addr ... }` | 移除 | 权重通过 EdgeAPI 的 NSRoute 机制获取 |

### 7.3 数据库

移除 PG 依赖后，本地不再需要 PostgreSQL。`--auto-migrate` 标志废弃。

---

## 8. 开发阶段规划

| 阶段 | 内容 | 预估工作量 |
|------|------|-----------|
| P1 | `internal/edgeapi/` 客户端（Token + Zone + Record CRUD） | 2 天 |
| P2 | 配置层改造（新增 `edgeapi` 块，移除 `postgres`/`nacos`） | 0.5 天 |
| P3 | EdgeAPI Syncer（全量同步，替换 PG Syncer） | 1 天 |
| P4 | GoEdge Provider API（`/NSDomainService/...` 路由组） | 1.5 天 |
| P5 | 鉴权中间件（AccessKeyId/Secret 校验，Token 颁发） | 0.5 天 |
| P6 | 集成测试 + 与 GoEdge 联调 | 2 天 |

---

## 9. 待确认事项

1. **EdgeAPI 地址**：部署在哪个域名/IP？需要 TLS？
2. **accessKeyId / accessKeySecret**：由 EdgeAPI 管理员在 EdgeAdmin 后台创建，需提前申请。
3. **权重（分流）**：GoEdge 的 NSRoute 机制与 dns-edge 现有 `weight` 字段如何映射？是否需要继续支持 `weight` 字段，还是完全交给 NSRoute 线路管理？
4. **启动时 EdgeAPI 不可达**：是拒绝启动，还是允许带空数据启动（待同步后生效）？
5. **写操作来源**：GoEdge 写操作通过 Provider API 进来，dns-edge 的内部 REST API 是否还允许直接写？若允许，如何同步到 EdgeAPI 中心？
