# dns-edge GoEdge Provider 接入方案

**版本**：v2.0  
**日期**：2026-06-25  
**分支**：`feature/goedge-provider`

---

## 1. 背景与目标

### 1.1 GoEdge 的 DNS 体系

GoEdge 是一套自托管 CDN 管理平台，由三个独立进程组成：

```
EdgeAdmin（Web 管理后台）
    │ gRPC
    ▼
EdgeAPI（中心管理服务，有自己的 MySQL）
    │ gRPC
    ▼
EdgeNode（边缘节点，做反向代理/缓存/WAF）
```

GoEdge 需要一个**权威 DNS 服务器**来托管 CDN 基础设施记录：

- 边缘节点 IP 的 A/AAAA 记录（节点加入/退出时自动更新）
- 用户域名的 CNAME 记录（指向对应集群）

GoEdge 通过 DNS Provider 接口管理这些记录，支持阿里云 DNS、Cloudflare、自定义 HTTP 等多种提供商。

### 1.2 dns-edge 的角色

dns-edge 作为 GoEdge 的 **DNS 提供商插件**，和阿里云 DNS 地位相同——GoEdge 通过 HTTP 接口调用 dns-edge 进行记录的增删改查，dns-edge 不是 EdgeNode，与 EdgeAPI 没有直连关系。

```
GoEdge EdgeAPI
  │  节点 IP 变更 / 用户域名新增
  │  调 DNS Provider 接口
  ▼
dns-edge（权威 DNS）
  │  写 PG + 更新内存
  ▼
全球各 dns-edge 节点（增量同步拉 PG）
```

### 1.3 目标

在 dns-edge 现有架构（PG + Nacos + 增量同步）**完全不变**的基础上，新增一套 GoEdge Provider HTTP 接口，让 GoEdge 能将 dns-edge 作为 DNS 提供商使用。

---

## 2. 架构说明

### 2.1 现有架构（保持不变）

```
┌──────────────────────────────────────────────────────────┐
│  dns-edge（每个边缘节点）                                  │
│                                                          │
│  [DNS Handler] ── ZoneStore（内存） ── [PG Syncer]        │
│       │                                     │            │
│  [内部 REST API]                      中心 PostgreSQL     │
│  /api/v1/...                                │            │
│                                      Nacos（权重）        │
└──────────────────────────────────────────────────────────┘
```

**全球节点数据同步**：所有边缘节点定时从中心 PG 增量拉取，PG 是唯一 source of truth。写操作打任意节点均可（写 PG + 更新本地内存），其他节点通过同步感知变更。

### 2.2 新增 GoEdge Provider 接口后的架构

```
GoEdge EdgeAPI
  │ 调 customHTTP Provider 接口（单个端点）
  ▼
┌──────────────────────────────────────────────────────────┐
│  dns-edge                                                │
│                                                          │
│  [DNS Handler] ── ZoneStore（内存） ── [PG Syncer]        │
│       │                                     │            │
│  [内部 REST API]    [GoEdge Provider API]  中心 PG        │
│  /api/v1/...        /goedge/dns（新增）      │            │
│                          │              Nacos（权重）     │
│                    写 PG + 更新内存                       │
└──────────────────────────────────────────────────────────┘
```

---

## 3. 接口选型：customHTTP vs edgeDNSAPI

GoEdge 提供两种接入方式，两者调用的是同一套 `ProviderInterface`，功能完全等价：

| | customHTTP（推荐） | edgeDNSAPI |
|---|---|---|
| HTTP 接口数量 | **1 个**端点，`action` 字段区分操作 | 14 个独立路径 |
| 鉴权方式 | `SHA1(secret@timestamp)` 签名 | AccessToken（需额外 token 颁发接口） |
| 实现工作量 | 低 | 较高 |
| GoEdge 官方文档 | 有 | 无单独文档 |
| 功能差异 | 无 | 无 |

**选择 customHTTP**，实现一个端点处理所有操作。

---

## 4. customHTTP 接口规范

### 4.1 鉴权

每次请求携带两个 Header：

```
Timestamp: <unix_timestamp>
Token: <sha1(secret + "@" + timestamp)>
```

GoEdge 配置 Provider 时填写 `url`（你的端点地址）和 `secret`（共享密钥）。

### 4.2 请求格式

所有操作均为 `POST`，Content-Type: application/json，Body 中通过 `action` 字段区分：

```json
{ "action": "<操作名>", ...其他参数 }
```

### 4.3 需实现的操作

| action | 请求参数 | 响应（直接返回 JSON，无外层包装） | 说明 |
|--------|----------|----------------------------------|------|
| `GetDomains` | — | `["example.com.", "foo.com."]` | 返回所有 zone 名称列表 |
| `GetRecords` | `domain` string | `[{id,name,type,value,route,ttl}]` | 返回指定 zone 的所有记录 |
| `GetRoutes` | `domain` string | `[{name,code}]` | 返回支持的线路列表（当前返回 `[{"name":"默认","code":"default"}]`） |
| `QueryRecord` | `domain` string, `name` string, `recordType` string | `{id,name,type,value,route,ttl}` 或 `null` | 查单条记录 |
| `QueryRecords` | `domain` string, `name` string, `recordType` string | `[{...}]` 或 `null` | 查多条记录 |
| `AddRecord` | `domain` string, `newRecord` object | — （HTTP 200 即成功） | 新建记录 |
| `UpdateRecord` | `domain` string, `record` object, `newRecord` object | — （HTTP 200 即成功） | 更新记录 |
| `DeleteRecord` | `domain` string, `record` object | — （HTTP 200 即成功） | 删除记录 |
| `DefaultRoute` | — | `"default"` （纯字符串，无 JSON 包装） | 返回默认线路 code |

Record 对象结构：

```json
{
  "id":    "123",
  "name":  "www.example.com.",
  "type":  "A",
  "value": "1.2.3.4",
  "route": "default",
  "ttl":   300
}
```

> **注意**：customHTTP 响应不需要外层 `{code, message, data}` 包装，直接返回业务数据，和 edgeDNSAPI 不同。

---

## 5. 写路径设计

GoEdge 调 `AddRecord` / `UpdateRecord` / `DeleteRecord` 时：

```
GoEdge → POST /goedge/dns { "action": "AddRecord", ... }
  │
  ▼
dns-edge GoEdge Provider 处理层
  ├─ 校验 Timestamp + Token 签名
  ├─ 转换为内部 iface.Record 格式
  ├─ 写入 PostgreSQL（通过现有 pg.Store）
  ├─ 更新本实例 ZoneStore 内存（热更新）
  └─ 返回 HTTP 200
         │
         ▼
    全球其他节点通过 PG 增量同步感知变更（30s 内）
```

---

## 6. 新增模块规划

只需新增一个文件，**不改动现有任何模块**：

### `internal/api/goedge_provider.go`

在现有 Gin 服务中新增一个路由：

```
POST /goedge/dns
```

处理逻辑：

```go
func (s *Server) goedgeProvider(c *gin.Context) {
    // 1. 验证 Timestamp + Token
    // 2. 解析 action
    // 3. switch action { case "GetDomains": ... case "AddRecord": ... }
    // 4. 调现有 s.pg / s.store 完成操作
}
```

所有操作复用现有的 `pg.Store`（`CreateRecord`、`SoftDeleteRecord` 等）和 `ZoneStore`，无需新增依赖。

---

## 7. 配置变化

**无需改动 Corefile**。GoEdge 那侧在 EdgeAdmin 后台配置 Provider 时填写：

- `url`：`http://<dns-edge-addr>:<api-port>/goedge/dns`
- `secret`：自定义共享密钥（在 dns-edge Corefile 的 `api` 块或环境变量中配置）

Corefile 中可选新增一个 `secret` 配置项用于验签：

```
api {
    listen  :28082
    goedge_secret  <your-shared-secret>
}
```

---

## 8. 开发阶段规划

| 阶段 | 内容 | 预估工作量 |
|------|------|-----------|
| P1 | `internal/api/goedge_provider.go`（单端点，全部 action 处理） | 1 天 |
| P2 | 鉴权中间件（Timestamp + Token SHA1 校验） | 0.5 天 |
| P3 | Corefile 新增 `goedge_secret` 配置项解析 | 0.5 天 |
| P4 | 单元测试（mock GoEdge 调用，覆盖各 action） | 1 天 |
| P5 | 与 GoEdge 联调测试 | 1 天 |

总计约 **4 天**，远少于 v1.0 方案的 7.5 天。

---

## 9. 待确认事项

1. **goedge_secret 配置方式**：放 Corefile 还是环境变量？
2. **权重（分流）**：GoEdge 调 `GetRoutes` 时，dns-edge 是否需要把 Nacos 里的权重线路暴露出去？还是只返回 `default` 一条线路，权重继续由 Nacos 内部管理？
3. **GoEdge 管理的记录范围**：GoEdge 只写节点 A 记录和用户 CNAME 记录，业务 DNS 记录（用户自己加的）仍通过 dns-edge 内部 REST API 管理，两者共存，GoEdge 不会覆盖业务记录。需确认 GoEdge 管理员和业务记录是否会有命名冲突风险。
