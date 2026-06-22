# dns-edge 技术设计文档

## 1. 背景与目标

### 1.1 背景

传统权威 DNS（BIND、NSD）的记录变更需要修改 Zone 文件并重新加载，操作繁琐且有短暂中断风险。在微服务、容器化场景下，后端 IP 频繁变更，需要一套能够实时、无重启地更新 DNS 记录的权威 DNS 服务。

同时，随着多 IDC、多可用区部署的普及，同一域名往往对应多个后端，需要根据后端的实时负载或健康状况动态调整流量分配比例。

### 1.2 目标

1. 实现一个符合 RFC 标准的权威 DNS 服务
2. 提供 HTTP REST API，支持 DNS 记录的实时热更新（无需重启）
3. 支持基于权重的流量分流，权重可由外部采样系统动态写入
4. 支持多实例水平扩展，实例间保持最终一致
5. 单二进制部署，容器化友好

---

## 1.3 项目定位

**dns-edge 是一个 `miekg/dns` 项目，不是 CoreDNS 项目。**

| 项目 | 说明 |
|------|------|
| `miekg/dns` | Go DNS 协议库，提供 RR 类型、ServeMux、PacketConn 等基础设施 |
| CoreDNS | 基于 miekg/dns 的插件化 DNS 框架，引入 plugin chain / coremain / Corefile 解析器 |
| dns-edge | **直接使用 miekg/dns**，不引入 CoreDNS plugin chain 和 coremain。仅可选借鉴 `coredns/caddy` 解析 Corefile 风格配置 |

dns-edge 的配置格式参考 Corefile 块级语法（可读性好），但底层直接调用 `miekg/dns` API 启动 DNS 服务，与 CoreDNS 框架无关。

---

### 2.1 系统拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│                          客户端                                   │
│              plain DNS (53)    DNS-over-TLS (853)                │
└──────────────┬────────────────────────┬────────────────────────-─┘
               │                        │
               ▼                        ▼
     ┌─────────────────────────────────────────┐
     │              dnsdist                     │
     │  TLS 终止 / 负载均衡 / 健康检查           │
     └───────────────────┬─────────────────────┘
                         │  plain DNS :5300
             ┌───────────┼───────────┐
             ▼           ▼           ▼
         ┌───────┐   ┌───────┐   ┌───────┐
         │ inst1 │   │ inst2 │   │ inst3 │   ← dns-edge 实例（多实例）
         └───┬───┘   └───┬───┘   └───┬───┘
             │           │           │
             └─────────────────┬─────┘
                               │
                    ┌──────────┴──────────┐
                    │                     │
                    ▼                     ▼
              PostgreSQL               Nacos
           （DNS 记录持久化）      （分流权重，ListenConfig 推送）
                    ▲
                    │ 写入 DataID 权重
            ┌───────┴────────┐
            │   采样系统      │  ← 独立服务，探测后端健康/延迟
            └────────────────┘
```

### 2.2 单实例内部结构

```
dns-edge 进程
├── DNS Server（UDP + TCP :5300）
│     ├── UDP：SO_REUSEPORT × N goroutine（各持独立 socket，内核分发包）
│     ├── TCP：goroutine-per-connection
│     └── QueryHandler
│           ├── 读 ZoneStore（内存，RWMutex / COW）
│           └── 读 WeightCache（内存，Nacos ListenConfig 回调更新）
│
├── HTTP API Server（:8080）
│     └── RecordHandler
│           ├── 写 PostgreSQL（先写，失败则 abort）
│           └── 写 ZoneStore（后写内存）
│
└── SyncScheduler（后台 goroutine）
      ├── 定时任务：每 30s 从 PG 增量拉取变更
      └── 概率任务：DNS 查询路径上 1% 概率触发同步（Token Bucket 限速）
```

---

## 3. 核心模块设计

### 3.1 ZoneStore（内存存储层）

ZoneStore 是所有 DNS 查询的直接数据源，必须保证高并发读性能。

**数据结构**

```go
type ZoneStore struct {
    mu    sync.RWMutex
    zones map[string]*Zone   // key: 域名（FQDN，带尾点）
}

type Zone struct {
    Name    string
    Records map[RecordKey][]*Record  // key: (name, type)
}

type Record struct {
    Name    string
    Type    uint16
    TTL     uint32
    Value   string
    Weight  int      // 分流权重，0 表示不参与分流
}
```

**并发策略（第一阶段：RWMutex）**

- 读操作（DNS 查询）：`RLock()`，允许并发读
- 写操作（热更新 API / PG 同步）：`Lock()`，独占写

DNS 场景读多写少（写操作为低频的 API 调用），`RWMutex` 的读锁本质上只是一次原子操作，在高并发读下竞争极低，第一阶段足够使用。

**并发策略（可选升级：atomic.Value COW）**

当压测发现 ZoneStore 锁成为瓶颈时，升级为 Copy-on-Write 模式，读路径零锁：

```go
type ZoneStore struct {
    snapshot atomic.Value   // 存储 map[string]*Zone 的指针
    mu       sync.Mutex     // 仅保护写路径，防止并发写产生竞争
}

// 读路径：一次原子 load，无锁
func (s *ZoneStore) Lookup(name string) *Zone {
    zones := s.snapshot.Load().(map[string]*Zone)
    return zones[name]
}

// 写路径：复制旧 map → 修改 → 原子替换
func (s *ZoneStore) Update(zone *Zone) {
    s.mu.Lock()
    defer s.mu.Unlock()
    old := s.snapshot.Load().(map[string]*Zone)
    next := make(map[string]*Zone, len(old))
    for k, v := range old {
        next[k] = v
    }
    next[zone.Name] = zone
    s.snapshot.Store(next)
}
```

> **注意**：不建议使用 `xsync.Map` 等第三方并发 Map。其价值在于高并发写不同 key 的场景（如计数器）；DNS ZoneStore 写入的是整个 Zone 对象且极低频，`xsync.Map` 在此无额外收益，反而引入外部依赖。

### 3.2 DNS QueryHandler

基于 `miekg/dns` 实现 `dns.Handler` 接口：

```go
func (h *Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
    q := r.Question[0]

    // 1. 查找记录
    records := h.store.Lookup(q.Name, q.Qtype)
    if records == nil {
        // NXDOMAIN
        ...
        return
    }

    // 2. 分流选择（如有多后端）
    selected := h.selectByWeight(records)

    // 3. 构造响应
    m := new(dns.Msg)
    m.SetReply(r)
    m.Authoritative = true
    for _, rr := range selected {
        m.Answer = append(m.Answer, rr)
    }
    w.WriteMsg(m)

    // 4. 概率触发 PG 同步
    if rand.Float64() < h.syncProb {
        go h.syncer.TriggerSync()
    }
}
```

**支持的记录类型**

| 类型 | RFC | 说明 |
|------|-----|------|
| A | RFC 1035 | IPv4 地址 |
| AAAA | RFC 3596 | IPv6 地址 |
| CNAME | RFC 1035 | 别名 |
| MX | RFC 1035 | 邮件交换 |
| TXT | RFC 1035 | 文本记录 |
| NS | RFC 1035 | 域名服务器 |
| SOA | RFC 1035 | 区域授权记录 |
| PTR | RFC 1035 | 反向解析 |
| SRV | RFC 2782 | 服务定位 |

### 3.3 流量分流

**权重来源优先级**（高优先级覆盖低优先级）

```
Nacos 动态权重  >  PG 静态权重  >  均等分配
```

**加权随机算法**

```go
func weightedRandom(records []*Record) *Record {
    total := 0
    for _, r := range records {
        total += r.Weight
    }
    n := rand.Intn(total)
    for _, r := range records {
        n -= r.Weight
        if n < 0 {
            return r
        }
    }
    return records[len(records)-1]
}
```

**Nacos 权重格式**

```
DataID:  dns_weights:{fqdn}:{type}      例：dns_weights:api.example.com.:A
Group:   DEFAULT_GROUP（可按环境配置）
Value:   {"1.2.3.4": 70, "5.6.7.8": 30}

获取方式：启动时 getConfig 全量拉取；ListenConfig 注册回调，Nacos 推送变更后毫秒级生效。
Nacos 不可用时自动降级为 PG 静态权重，PG 也无权重则均等分配。
```

### 3.4 HTTP API

基于 `gin` 实现，提供 DNS 记录的 CRUD 操作。

**写操作流程**

```
请求 → 参数校验 → 写 PostgreSQL → 更新内存 ZoneStore → 响应 200
                        │
                   失败时返回 500，不更新内存
```

**接口列表**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/domains` | 列出所有域名 |
| POST | `/api/v1/domains` | 添加域名 |
| DELETE | `/api/v1/domains/:domain` | 删除域名及所有记录 |
| GET | `/api/v1/domains/:domain/records` | 列出域名下所有记录 |
| POST | `/api/v1/domains/:domain/records` | 添加记录 |
| PUT | `/api/v1/domains/:domain/records/:id` | 更新记录 |
| DELETE | `/api/v1/domains/:domain/records/:id` | 删除记录 |
| GET | `/healthz` | 健康检查（liveness probe）|
| GET | `/metrics` | Prometheus 指标（Prometheus scraper）|

**请求体示例（添加记录）**

```json
{
  "name": "www.example.com.",
  "type": "A",
  "ttl": 300,
  "value": "1.2.3.4"
}
```

**分流记录（多后端）**

```json
{
  "name": "api.example.com.",
  "type": "A",
  "ttl": 10,
  "backends": [
    {"value": "1.2.3.4", "weight": 70},
    {"value": "5.6.7.8", "weight": 30}
  ]
}
```

### 3.5 PostgreSQL 数据模型

```sql
-- 域名表
CREATE TABLE domains (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,   -- FQDN，带尾点，如 example.com.
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ            -- 软删除
);

-- 记录表
CREATE TABLE records (
    id         BIGSERIAL PRIMARY KEY,
    domain_id  BIGINT NOT NULL REFERENCES domains(id),
    name       TEXT NOT NULL,          -- FQDN
    type       TEXT NOT NULL,          -- A / AAAA / CNAME / MX ...
    ttl        INTEGER NOT NULL DEFAULT 300,
    value      TEXT NOT NULL,
    weight     INTEGER NOT NULL DEFAULT 100,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ            -- 软删除

    INDEX idx_records_domain_id (domain_id),
    INDEX idx_records_updated_at (updated_at)
);
```

**增量同步查询**

```sql
SELECT r.*, d.name as domain_name
FROM records r
JOIN domains d ON r.domain_id = d.id
WHERE r.updated_at > $1
   OR d.updated_at > $1
ORDER BY r.updated_at ASC;
```

### 3.6 多实例同步

**定时同步（30s 间隔）**

```
goroutine 启动 → 记录 last_sync_at
循环：
  sleep(30s)
  SELECT * FROM records WHERE updated_at > last_sync_at
  批量更新内存 ZoneStore
  更新 last_sync_at
```

**概率触发同步（1% 概率）**

在 `ServeDNS` 路径末尾，以 1% 概率异步触发一次增量同步。这使得高 QPS 场景下（10k QPS）平均每秒约 100 次 PG 查询，需配合速率限制（token bucket）避免突发流量打满 PG。

**一致性保证**

| 场景 | 最大不一致窗口 |
|------|----------------|
| API 写入本实例 | 0ms（直接更新内存） |
| 其他实例定时同步 | ≤ 30s |
| 其他实例概率触发 | ≤ 几秒（高 QPS 下） |

这是**最终一致性**模型，与 DNS TTL 机制天然兼容（下游 resolver 本身就会缓存记录到 TTL 过期）。

### 3.7 AXFR Zone Transfer

多实例部署时，slave 节点可通过 AXFR 协议从 master 拉取完整 Zone 数据，作为 PG 增量同步的补充。

`miekg/dns` 原生支持 AXFR，实现逻辑：接收到 AXFR 查询时，从 ZoneStore 序列化完整 Zone，按协议格式分多包返回。

SOA serial 规则：每次 API 写操作后，对应 Zone 的 SOA serial 递增（使用 Unix 时间戳格式：`YYYYMMDDnn`）。slave 节点通过对比 serial 决定是否触发 AXFR。

---

## 8. 设计原则

### 8.1 模块间只依赖接口

所有跨模块调用通过 Go interface 而非具体类型传递。切换底层实现只需改启动时的依赖注入，调用方代码零修改。

**核心接口（待实现时细化）**

```go
// ZoneStore — DNS 查询和热更新的核心存储
// 第一阶段实现：RWMutexStore
// 可选升级：COWStore（atomic.Value，读路径零锁）
type ZoneStore interface {
    Lookup(name string, qtype uint16) []*Record
    Update(zone *Zone) error
    Delete(name string) error
    Snapshot() map[string]*Zone   // 供 AXFR 使用
}

// WeightProvider — 分流权重来源
// 实现选项：
//   NacosWeightProvider    — Nacos ListenConfig 推送，毫秒级感知变更（主）
//   StaticWeightProvider   — 从 ZoneStore 读静态权重（PG 持久化，降级用）
//   CompositeWeightProvider — Nacos 优先，Nacos 不可用时自动降级静态权重
type WeightProvider interface {
    GetWeights(fqdn string, qtype uint16) map[string]int
}

// Syncer — PG 增量同步
// 解耦同步策略（定时/概率）与存储实现
type Syncer interface {
    TriggerSync() error
    Start(ctx context.Context)
}
```

**依赖方向**

```
cmd/dns-edge (main)
    │  注入具体实现
    ├── dns.Handler      依赖 ZoneStore + WeightProvider
    ├── api.Handler      依赖 ZoneStore + pg.Store
    └── sync.Scheduler   依赖 ZoneStore + pg.Store
```

各模块不 import 兄弟模块的包，只 import 公共 interface 包。

### 8.2 配置格式（Corefile 风格）

参考 CoreDNS 的 Corefile 块级语法，配置分组清晰，每个模块只读自己的块：

```
dns-edge {
    listen   :5300
    workers  0          # SO_REUSEPORT worker 数；0=禁用，-1=NumCPU，默认 0
    tcp      true
    edns0    true       # 启用 EDNS0，UDP 上限 4096B

    api {
        listen :8080
    }

    postgres {
        dsn      postgres://user:pass@host:5432/db?sslmode=require
    }

    nacos {
        addr        nacos:8848
        namespace   ""           # 命名空间 ID（默认 public）
        group       DEFAULT_GROUP
        data_id_prefix  dns_weights:  # DataID 格式：dns_weights:{fqdn}:{type}
    }

    sync {
        interval  30s
        prob      0.01
        ratelimit 100       # 概率触发最大 QPS
    }
}
```

`workers` 字段直接控制 SO_REUSEPORT 的 goroutine 数量，第一阶段设 `0` 禁用，压测后按需开启。

---

### 4.1 单机部署

```
:853 (DoT)  :53 (plain)
      │            │
      └────────────┘
            │
         dnsdist (:5300 → localhost)
            │
         dns-edge (:5300, :8080)
            │
         PostgreSQL + Nacos (本机或远端)
```

### 4.2 多实例部署（推荐）

```
                  LB (dnsdist)
                 /      |      \
           inst1       inst2    inst3
              \          |       /
               \─────────┼──────/
                         │
                     PostgreSQL (主从或云托管)
                         │
                       Nacos (集群，已有基础设施)
```

实例间无需互相通信，均独立读 PG / Nacos，通过定时同步保持最终一致。

### 4.3 Docker 镜像

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o dns-edge ./cmd/dns-edge

FROM scratch
COPY --from=builder /build/dns-edge /dns-edge
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/dns-edge"]
```

目标镜像大小：< 20MB。

### 4.4 环境变量 / 配置

```yaml
# config.yaml
dns:
  listen: ":5300"
  tcp: true

api:
  listen: ":8080"

pg:
  dsn: "postgres://user:pass@host:5432/dnsdb?sslmode=require"

nacos:
  addr: "nacos:8848"
  namespace: ""              # 命名空间 ID，默认 public
  group: "DEFAULT_GROUP"
  data_id_prefix: "dns_weights:"  # DataID = prefix + fqdn + ":" + type

sync:
  interval: "30s"
  prob: 0.01
  rate_limit: 100            # 概率触发最大 QPS（防止打爆 PG）
```

---

## 5. 性能设计

### 5.1 性能预估

| 指标 | 预估值 | 说明 |
|------|--------|------|
| DNS 查询 QPS | 50,000+/实例 | miekg/dns + 内存查询，无 I/O |
| 热更新延迟 | < 5ms | PG 写入 + 内存更新 |
| 同实例一致延迟 | 0ms | 直接更新内存 |
| 跨实例一致延迟 | ≤ 30s | 定时同步 |
| 内存占用 | ~50MB/百万条记录 | 估算，视记录长度而定 |

### 5.2 性能优化策略

按优先级排列，以下优化均已纳入设计，按需逐步启用。

#### 优化一：ZoneStore 读路径零锁（atomic.Value COW）

**问题**：`RWMutex` 在极高 QPS 下读锁仍有原子操作开销，且写操作会短暂阻塞所有读者。

**方案**：用 `atomic.Value` 保存 Zone Map 快照，读时直接 load 指针，无任何锁操作；写时复制 Map、改完后 atomic store 替换。

**适用时机**：压测发现 ZoneStore 锁竞争明显时升级，默认第一阶段用 RWMutex。

```go
// 读路径：零锁，一次原子 load
zones := s.snapshot.Load().(map[string]*Zone)

// 写路径：复制 → 修改 → 原子替换（写操作本身加 Mutex，防并发写）
```

#### 优化二：DNS 消息 Buffer 复用（sync.Pool）

**问题**：每次 DNS 查询都需要分配、序列化、GC 一个 `dns.Msg` 和字节缓冲区，高 QPS 下 GC 压力显著。

**方案**：用 `sync.Pool` 复用 `[]byte` 缓冲区，减少堆分配和 GC 停顿。

```go
var bufPool = sync.Pool{
    New: func() any {
        buf := make([]byte, dns.DefaultMsgSize)
        return &buf
    },
}

func handler(w dns.ResponseWriter, r *dns.Msg) {
    buf := bufPool.Get().(*[]byte)
    defer bufPool.Put(buf)
    // 使用 buf 序列化响应
}
```

**收益**：减少 GC 频率，降低 P99 延迟抖动。

#### 优化三：EDNS0 支持（减少 TCP Fallback）

**问题**：标准 DNS UDP 响应限制 512 字节，超出时客户端会 TCP 重试，增加延迟和服务端连接数。

**方案**：支持 EDNS0（RFC 6891），将 UDP 响应上限扩展至 4096 字节，覆盖绝大多数 MX/TXT 等大记录场景，大幅减少 TCP fallback。

```go
// 响应中加入 OPT RR 声明 EDNS0 支持
opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
opt.SetUDPSize(4096)
m.Extra = append(m.Extra, opt)
```

#### 优化四：UDP 多 Socket 并发读（SO_REUSEPORT）

**问题**：miekg/dns 默认单个 UDP socket，由一个 goroutine 读包后派发给 handler goroutine，单 socket 读取是串行瓶颈，多核利用率低。

**方案**：`SO_REUSEPORT` 允许多个 socket 同时绑定同一端口，内核将 UDP 包哈希分发到各 socket。多个 goroutine 各持一个 socket 并发读，彻底消除读取串行瓶颈。

**注意**：对外仍是单一端口 `:5300`，多 socket 对客户端完全透明。

```go
// 对外只有一个 :5300 端口
// 内核将 UDP 包分散到多个 socket
// ┌── socket1 → goroutine1 → handler
// ├── socket2 → goroutine2 → handler   ← 内核负载均衡
// └── socket3 → goroutine3 → handler

lc := net.ListenConfig{
    Control: func(network, address string, c syscall.RawConn) error {
        return c.Control(func(fd uintptr) {
            syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET,
                syscall.SO_REUSEPORT, 1)
        })
    },
}
// 启动 runtime.NumCPU() 个 goroutine，各自 ListenPacket(":5300") + dns.Server
```

**适用时机**：单机 QPS 超过 10 万后考虑启用，前期用默认模式即可。

### 5.3 优化启用建议

```
第一阶段（开发期）：RWMutex + 默认单 socket
    ↓  压测 QPS 瓶颈
第二阶段：启用 SO_REUSEPORT + sync.Pool + EDNS0
    ↓  发现 GC / 锁竞争
第三阶段：升级 atomic.Value COW
```

> 过早优化是万恶之源。先保证正确性，瓶颈出现在哪优化哪。

---

## 6. 依赖版本

| 库 | 版本 | 用途 |
|----|------|------|
| `github.com/miekg/dns` | v1.1.x | DNS 协议实现 |
| `github.com/gin-gonic/gin` | v1.10.x | HTTP API |
| `github.com/jackc/pgx/v5` | v5.x | PostgreSQL 驱动 |
| `github.com/nacos-group/nacos-sdk-go/v2` | v2.x | Nacos 配置中心客户端 |
| `github.com/prometheus/client_golang` | v1.12.x | Prometheus 指标 |
| `go.uber.org/zap` | v1.x | 结构化日志 |

---

## 7. 后续扩展方向

- **采样系统集成**：独立服务探测后端延迟/成功率，按公式计算权重写入 Nacos DataID（`dns_weights:{fqdn}:{type}`）
- **DNSSEC**：由 dnsdist 层处理，权威 DNS 不实现签名
- **Web 控制台**：可视化管理 DNS 记录和分流权重

---

## 8. Prometheus 指标参考

指标通过 HTTP API 端口（默认 `:8080`）的 `/metrics` 路径暴露，格式为 Prometheus text exposition format。

### 8.1 DNS 查询指标

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `dns_queries_total` | Counter | `qtype`, `rcode` | 接收到的 DNS 查询总数（按查询类型和响应码分组）|
| `dns_query_duration_seconds` | Histogram | `qtype` | DNS 查询处理延迟分布（桶：50μs / 100μs / 200μs / 500μs / 1ms / 5ms / 10ms）|

**常用 PromQL**

```promql
# 每秒 QPS（按查询类型）
rate(dns_queries_total[1m])

# P99 查询延迟
histogram_quantile(0.99, rate(dns_query_duration_seconds_bucket[5m]))

# NXDOMAIN 比例（可用于检测域名探测攻击）
rate(dns_queries_total{rcode="NXDOMAIN"}[5m])
  / ignoring(rcode) sum without(rcode)(rate(dns_queries_total[5m]))
```

### 8.2 同步指标

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `dns_sync_total` | Counter | `result` (`success`/`error`) | 增量 PG 同步次数 |
| `dns_sync_duration_seconds` | Histogram | — | 成功同步的耗时分布 |
| `dns_sync_last_success_timestamp_seconds` | Gauge | — | 最后一次成功同步的 Unix 时间戳（值为 0 表示启动后从未同步成功）|

**告警规则建议**

```yaml
# Prometheus alerting rules
groups:
  - name: dns-edge
    rules:
      # 5 分钟内无成功同步 → 可能 PG 不可达或 Token Bucket 持续限速
      - alert: DNSSyncStale
        expr: time() - dns_sync_last_success_timestamp_seconds > 300
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "dns-edge 实例 {{ $labels.instance }} 同步停滞超过 5 分钟"

      # 同步错误率超过 10%
      - alert: DNSSyncErrorRate
        expr: |
          rate(dns_sync_total{result="error"}[5m])
          / rate(dns_sync_total[5m]) > 0.1
        for: 3m
        labels:
          severity: critical
```

### 8.3 Prometheus 抓取配置

```yaml
# prometheus.yml 抓取配置
scrape_configs:
  - job_name: dns-edge
    static_configs:
      - targets:
          - dns-edge-1:8080
          - dns-edge-2:8080
          - dns-edge-3:8080
    metrics_path: /metrics
    scrape_interval: 15s
```

---

## 9. 运维参考

### 9.1 Kubernetes 部署建议

**Deployment 要点**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dns-edge
spec:
  replicas: 3
  strategy:
    rollingUpdate:
      maxUnavailable: 1   # 保证至少 2/3 实例在线
  template:
    spec:
      containers:
        - name: dns-edge
          image: your-registry/dns-edge:latest
          ports:
            - containerPort: 53
              protocol: UDP
              name: dns-udp
            - containerPort: 53
              protocol: TCP
              name: dns-tcp
            - containerPort: 8080
              protocol: TCP
              name: http
          volumeMounts:
            - name: config
              mountPath: /etc/dns-edge
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
          resources:
            requests:
              cpu: 200m
              memory: 128Mi
            limits:
              cpu: 2000m
              memory: 512Mi
      volumes:
        - name: config
          configMap:
            name: dns-edge-config
```

**ConfigMap（Corefile）**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: dns-edge-config
data:
  Corefile: |
    dns-edge {
        listen   :53
        tcp      true
        edns0    true

        api {
            listen :8080
        }

        postgres {
            dsn  $(PG_DSN)   # 从环境变量注入
        }

        nacos {
            addr        $(NACOS_ADDR)
            namespace   $(NACOS_NAMESPACE)
            group       DEFAULT_GROUP
            data_id_prefix  dns_weights:
        }

        sync {
            interval  30s
            prob      0.01
            ratelimit 100
        }
    }
```

**Service（DNS 端口）**

```yaml
# DNS 服务需要 LoadBalancer 类型，或通过 hostNetwork: true + DaemonSet 绑定宿主机端口
apiVersion: v1
kind: Service
metadata:
  name: dns-edge
spec:
  type: LoadBalancer
  ports:
    - name: dns-udp
      port: 53
      protocol: UDP
      targetPort: 53
    - name: dns-tcp
      port: 53
      protocol: TCP
      targetPort: 53
    - name: http
      port: 8080
      protocol: TCP
      targetPort: 8080
```

> **注意**：Kubernetes Service 对 UDP + TCP 同端口的支持因 CNI 插件而异，建议测试后确认。dnsdist 前置场景可只暴露 TCP/UDP :5300，不直接暴露 :53。

### 9.2 Helm Chart 骨架

```
charts/dns-edge/
  Chart.yaml
  values.yaml          # 暴露 replicas / image / pgDsn / nacosAddr 等
  templates/
    deployment.yaml
    service.yaml
    configmap.yaml      # 从 values 模板生成 Corefile
    serviceaccount.yaml
    prometheusrule.yaml # 可选：PrometheusRule CRD
```

**values.yaml 关键字段**

```yaml
replicaCount: 3

image:
  repository: your-registry/dns-edge
  tag: latest
  pullPolicy: IfNotPresent

config:
  listen: ":53"
  tcp: true
  edns0: true
  sync:
    interval: "30s"
    prob: 0.01
    rateLimit: 100

postgres:
  dsn: ""   # 通过 --set 或 ExternalSecret 注入，不写入 Chart

nacos:
  addr: ""
  namespace: ""
  group: "DEFAULT_GROUP"
  dataIdPrefix: "dns_weights:"

resources:
  requests:
    cpu: 200m
    memory: 128Mi
  limits:
    cpu: 2000m
    memory: 512Mi

serviceMonitor:
  enabled: true   # 需要 prometheus-operator
  interval: 15s
```

### 9.3 性能调优决策树

```
单实例 QPS < 50,000？
  → 默认配置即可（RWMutex + 单 socket）

QPS 50,000–150,000？
  → 启用 SO_REUSEPORT（Corefile workers 设为 CPU 核数）
  → 确认 edns0 true 已开启（减少 TCP fallback）
  → 检查 GC 停顿：go tool pprof + /debug/pprof/allocs

QPS > 150,000 或 GC 停顿 > 1ms？
  → 升级 ZoneStore 为 atomic.Value COW（消除读锁竞争）
  → 加入 sync.Pool 复用 dns.Msg 缓冲区

写路径热更新 > 500 次/秒？
  → PutRecord COW ~2.6μs；批量更新比逐条更新效率高
  → 考虑攒批（debounce 100ms）后一次性更新内存
```

### 9.4 基准数据摘要（参考值，amd64 2.5GHz Xeon）

| 场景 | 延迟 | 每次分配数 |
|------|------|-----------|
| A 查询（含 RWMutex Lookup）| ~500 ns | 5 |
| NXDOMAIN（含 NameExists + SOA 合成）| ~560 ns | 8 |
| CNAME 追踪（2 次 Lookup）| ~440 ns | 7 |
| Lookup 纯读路径 | ~85 ns | **0** |
| Lookup 并发（8 goroutine）| ~70 ns | 0 |
| PutRecord COW 替换 | ~2.6 μs | 19 |
| 单核理论最大 QPS（500 ns/query）| ~2,000,000 | — |

> EDNS0 开启后响应包含额外 OPT RR，序列化时间增加约 5–10 ns，可忽略不计。
- **gRPC API**：作为 HTTP API 的补充，供内部服务调用
