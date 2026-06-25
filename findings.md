# 研究与发现

## 方案 A：PowerDNS + Lua

### 核心机制
- PowerDNS 通过 **Backend API** 将查询委托给外部后端，Lua 是最灵活的扩展点
- `pdns_server` 支持 `--launch=lua` 加载 Lua 脚本作为 Backend
- Lua 脚本实现 `dns_lookup()` / `list()` / `get()` 等回调函数
- **热更新路径**：Lua 脚本可以访问外部数据源（Redis、共享内存、HTTP），PowerDNS 自带 HTTP API（端口 8081）支持远程操作

### PowerDNS 自带 HTTP API 能力
- `GET /api/v1/servers/localhost/zones` — 列出 Zone
- `PATCH /api/v1/servers/localhost/zones/{zone}` — 修改 Zone 记录（热更新）
- 需要在 pdns.conf 中开启 `webserver=yes` 和 `api=yes`

### Lua Backend 热更新方案
```lua
-- pdns-backend.lua
local cache = {}  -- 内存缓存

function dns_lookup(qname, qtype)
  -- 从共享存储读取（可以是 Redis / 文件 / HTTP）
  return cache[qname] or {}
end

-- HTTP 触发 reload：外部 API 调用后更新 cache
```

### 优点
- PowerDNS 本身已经过生产验证，稳定性极高
- 自带 HTTP API，热更新功能几乎开箱即用
- Lua 脚本轻量，可热加载

### 缺点
- 需要安装 `pdns_server`（环境当前未安装）
- Lua 调试生态弱，类型不安全
- 与主代码库（可能是 Go）割裂，运维复杂度增加
- C++ + Lua 混合栈，团队门槛较高

---

## 方案 B：CoreDNS 插件框架（推荐方向）

### CoreDNS 插件机制
- CoreDNS 本质是一个 **插件链**，每个 DNS 请求经过一组 Handler 处理
- 插件实现 `plugin.Handler` 接口（`ServeDNS` 方法）
- 插件可注册到 `plugin.cfg`，也可以**不注册、直接作为库使用**

### 作为框架使用（类 Gin 模式）
```go
// 核心思路：不使用 CoreDNS 的 main()，而是引入其库
import (
    "github.com/coredns/coredns/core/dnsserver"
    "github.com/coredns/coredns/plugin"
    _ "github.com/coredns/coredns/plugin/forward"
    // 只引入需要的插件
)

// 自定义启动，嵌入自己的 authoritative 插件 + HTTP API
```

- `github.com/coredns/coredns` 作为 Go module 引入
- 自定义插件实现权威解析逻辑 + 内存 Zone 存储
- 同进程内嵌入 Gin HTTP 服务，共享内存 Zone 数据

### 热更新核心设计
```
                    ┌─────────────────────────────┐
  DNS 查询 ──────►  │  CoreDNS Plugin Chain       │
                    │  [authoritative plugin]      │──► ZoneStore (sync.Map / RWMutex)
  HTTP API ───────► │  [Gin HTTP Server]           │──► ZoneStore (热更新写入)
                    └─────────────────────────────┘
```

### 关键 Go 库
- `github.com/coredns/coredns` — DNS 框架
- `github.com/miekg/dns` — CoreDNS 底层 DNS 库（也可直接用）
- `github.com/gin-gonic/gin` — HTTP API
- `sync.RWMutex` 或 `sync/atomic` — 并发安全 Zone 存储

### 更简化路径：直接用 miekg/dns
- `github.com/miekg/dns` 是 CoreDNS 的底层库，可以**完全绕过 CoreDNS**
- 自己实现 DNS Server + Handler，完全控制
- CoreDNS 的价值在于：插件生态（forwarder、cache、health 等）可复用

### 优点
- 全 Go 技术栈，与环境完美匹配（Go 1.25 已安装）
- 单二进制部署，无外部依赖
- HTTP API 与 DNS 服务共享内存，热更新延迟趋近于零
- 类型安全，可观测性好（pprof、metrics 天然集成）
- 可直接用 miekg/dns 进一步简化

### 缺点
- 需要手动实现部分 PowerDNS 已有的功能（DNSSEC 等）
- CoreDNS "作为库" 的用法文档较少，需要读源码

---

## 热更新 API 设计草案

### 接口列表
| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/api/v1/zones` | 列出所有 Zone |
| POST | `/api/v1/zones` | 创建 Zone |
| GET | `/api/v1/zones/{zone}` | 查询 Zone 详情 |
| DELETE | `/api/v1/zones/{zone}` | 删除 Zone |
| GET | `/api/v1/zones/{zone}/records` | 列出记录 |
| POST | `/api/v1/zones/{zone}/records` | 添加记录（热更新） |
| PUT | `/api/v1/zones/{zone}/records/{name}/{type}` | 更新记录 |
| DELETE | `/api/v1/zones/{zone}/records/{name}/{type}` | 删除记录 |

---

## 部署架构：dnsdist 前置 DoT

### 确认可行
dnsdist 作为 DoT/DoH/DoQ 前置代理是业界成熟方案（BIND/NSD/PowerDNS 均采用此模式）。

```
客户端 --DoT(853)--> dnsdist (TLS 终止) --plain DNS(5300)--> 权威 DNS (本项目)
```

- 权威 DNS 只需监听 localhost:5300，无需实现 TLS
- dnsdist 提供 TLS 终止 + 负载均衡 + 健康检查，天然支持多实例
- **结论：DNSSEC 签名不需要在权威 DNS 内实现**

### dnsdist 最小配置
```lua
setACL({'0.0.0.0/0', '::/0'})
addLocal('0.0.0.0:53', {doTCP=true})
addTLSLocal('0.0.0.0:853', '/path/to/cert.pem', '/path/to/key.pem', {provider='openssl'})
newServer({address='127.0.0.1:5300', checkInterval=5})
```

参考：[dnsdist DoT 官方文档](https://dnsdist.org/guides/dns-over-tls.html) | [2024 实战教程](https://blog.christoffer.online/posts/2024-05-05-setup-dnsdist-as-dot-doh-doq-frontend-for-an-authoritative-server/)

---

## 持久化方案

- **存储**：PostgreSQL
- 启动时从 PG 加载全量 Zone 数据到内存
- 热更新写入：先写 PG，再更新内存 ZoneStore（保证重启后数据不丢）
- 多实例场景：各实例独立内存，通过 PG 作为 source of truth，变更通知可用 PG LISTEN/NOTIFY 或 API 广播

---

## 环境信息
- OS: Linux 6.8.0-1057-aws
- Go: 1.25.11 linux/amd64
- Python: 3.10.12
- PowerDNS: 未安装
- CoreDNS: 未安装（但 Go 环境可直接 go get）

### 并发安全
- DNS 查询是高并发读，API 更新是低频写
- 推荐：`sync.RWMutex` 包裹 Zone map，或 `atomic.Value` COW 模式

### SOA serial 自增
- 每次热更新后自动递增 SOA serial，否则 slave 不会触发 AXFR 同步

### AXFR / Zone Transfer（需要实现）
- 多实例部署，需要实现 AXFR 协议供 slave 节点同步
- miekg/dns 库原生支持 AXFR，实现成本不高

### 多实例热更新一致性（确认方案）
- **写路径**：API 写操作同时写 PG + 更新本实例内存（双写）
- **读同步**：其他实例通过两种机制从 PG 拉取最新数据：
  1. **定时轮询**：每隔固定间隔（如 30s）全量或增量拉取 PG
  2. **概率触发**：每次 DNS 查询以一定概率（如 1%）触发拉取，分散压力、降低延迟
- **一致性特征**：最终一致，短暂窗口内各实例数据可能不同，适合 DNS 场景（TTL 本身就有缓存容忍）
- **增量拉取建议**：PG 记录中增加 `updated_at` 字段，拉取时只同步上次拉取时间之后的变更

---

## GoEdge Provider 接口分析（2026-06-24）

### 来源
`TeaOSLab/EdgeAPI` — `internal/dnsclients/provider_edge_dns_api.go`

### 核心发现
GoEdge 通过 `ProviderInterface` 操作 DNS 提供商，对接方式是 HTTP POST + JSON，统一响应 `{code, message, data}`。

**鉴权**：POST `/APIAccessTokenService/getAPIAccessToken`，换取带 `expiresAt` 的 token，后续请求放 `X-Edge-Access-Token` header，到期前 600s 刷新。

**需实现接口（14 个）**：
- Zone：ListNSDomains、FindNSDomainWithName
- Record：ListNSRecords、FindNSRecordWithNameAndType、FindNSRecordsWithNameAndType、CreateNSRecord、UpdateNSRecord、DeleteNSRecord
- Route：FindAllDefaultWorldRegionRoutes、FindAllDefaultChinaProvinceRoutes、FindAllDefaultISPRoutes、FindAllAgentNSRoutes、FindAllNSRoutes

**线路**：GoEdge 调用 `FindAllAgentNSRoutes` 失败会忽略（源码注释明确），dns-edge 暂时所有 Route 接口返回空列表。

**写路径**：GoEdge 调 CreateNSRecord → dns-edge 透传到 EdgeAPI 中心写入 → 更新本地内存。

### 架构影响
- 移除 PG 直连（数据来源改为 EdgeAPI 拉取）
- 移除 Nacos 直连（权重配置改走 EdgeAPI NSRoute 机制）
- 无增量接口，改为定时全量同步（30s）

