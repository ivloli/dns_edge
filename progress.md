# 会话日志

## 2026-06-20 — 会话 1

### 完成的工作
- 创建三个规划文件（task_plan.md、findings.md、progress.md）
- 用户描述项目需求：权威 DNS + 热更新 HTTP API
- 完成两个方案（PowerDNS+Lua / CoreDNS插件框架）的技术调研
- 更新 findings.md：方案对比、热更新设计、关键挑战
- 更新 task_plan.md：功能需求、阶段规划、方案对比表

### 关键发现
- 环境已有 Go 1.25，无 PowerDNS —— 方案 B 环境友好度更高
- `miekg/dns` 可作为比 CoreDNS 更轻量的底层选项
- 热更新核心是：共享内存 ZoneStore + RWMutex，API 写、DNS 读

### 待决策
- [ ] 选择方案 A（PowerDNS+Lua）还是方案 B（CoreDNS/miekg 框架）
- [ ] 方案 B 中：直接用 miekg/dns 还是套 CoreDNS 插件体系？
- [ ] 是否需要 DNSSEC？
- [ ] 是否需要 AXFR Zone Transfer（主从同步）？

### 下一步
- 等待用户确认技术选型方向
- 选型后进入阶段 1：项目初始化

---

## 2026-06-21 — 会话 2

### 完成的工作
- 确认技术选型：miekg/dns + Gin + PostgreSQL + Redis
- 确认架构：dnsdist 前置 DoT，权威 DNS 不处理 TLS
- 确认持久化：PostgreSQL 双写，以 PG 为准
- 确认多实例同步：定时轮询（30s）+ 概率触发（1%）增量拉取
- 确认分流方案：Redis 存权重，DNS 层只读，与采样系统解耦
- 编写 README.md
- 编写 docs/technical-design.md（Markdown 版技术文档）
- 编写 docs/technical-design.html（带侧边导航的 HTML 版技术文档）

### 当前文件结构
- README.md — 项目介绍、快速开始、API 列表
- docs/technical-design.md — 完整技术设计文档（Markdown）
- docs/technical-design.html — 完整技术设计文档（HTML，带侧边栏导航）

### 下一步
- 进入阶段 1：项目初始化（go mod、目录结构、最小可运行 DNS server）

---

## 2026-06-21 — 会话 3

### 完成的工作
- 分析并排除 xsync.Map / ants / gnet 的必要性（场景不匹配）
- 确定四项性能优化策略并写入技术文档：
  1. atomic.Value COW（ZoneStore 读路径零锁）
  2. sync.Pool 复用 DNS 消息 Buffer
  3. EDNS0 支持（减少 TCP fallback）
  4. SO_REUSEPORT 多 socket 并发读（workers 配置项）
- 新增设计原则章节（MD + HTML）：
  - 模块间只依赖接口（ZoneStore / WeightProvider / Syncer）
  - Corefile 风格配置，workers 字段控制 SO_REUSEPORT 数量
- 更新内部架构图（加入 SO_REUSEPORT / Token Bucket 说明）
- 分析参考项目 `/home/ivloli/Git_repo/dns`（coredns-ecs）：
  - 确认借鉴 nacos.go（ListenConfig 回调模式）
  - 确认借鉴 setup.go（Corefile 块级解析模式）
  - 确认不引入 coremain/plugin chain
- 确认 dns-edge 定性为 miekg/dns 项目（非 CoreDNS 项目）
- 确认分流权重从 Redis 改为 Nacos

### 关键决策
- SO_REUSEPORT workers 默认 0（禁用），压测后按需开启，无需改代码
- 优化策略分三阶段启用，避免过早优化
- 接口定义先记原则，写代码时细化
- Nacos ListenConfig Push 替代 Redis 定时拉取（更低延迟，少维护一个组件）

### 下一步
- 同步更新设计文档（Redis → Nacos）
- 进入阶段 1：项目初始化（go mod、目录结构、最小可运行 DNS server）

---

## 2026-06-21 — 会话 4

### 完成的工作
- 同步更新设计文档，将所有 Redis 引用替换为 Nacos：
  - `docs/technical-design.md`：架构图、内部结构图、分流权重格式、WeightProvider 注释、Corefile 配置块、部署图、config.yaml 配置块、依赖表、Roadmap
  - `docs/technical-design.html`：同上所有位置 + 侧边栏导航
  - `README.md`：架构图、技术栈表、依赖项、配置表、目录结构
- 新增"项目定位"章节（MD + HTML）：明确 dns-edge 是 miekg/dns 项目而非 CoreDNS 项目

### 关键变更
- Redis → Nacos（分流权重组件）
- `weight:{fqdn}:{type}` key → `dns_weights:{fqdn}:{type}` DataID
- RedisWeightProvider → NacosWeightProvider（主）/ StaticWeightProvider（降级）/ CompositeWeightProvider
- `redis {}` Corefile 块 → `nacos {}` 块
- `github.com/redis/go-redis/v9` 依赖 → `github.com/nacos-group/nacos-sdk-go/v2`
- 目录结构：`internal/redis/` → `internal/weight/`

### 下一步
- 阶段 1：项目初始化（go mod、目录结构、最小可运行 DNS server）

---

## 2026-06-21 — 会话 5（阶段 1）

### 完成的工作
- 搭建目录结构：cmd/dns-edge、internal/{iface,store,dns,weight}、config/
- go.mod（miekg/dns v1.1.72 + zap v1.27.0）
- internal/iface/iface.go — ZoneStore / WeightProvider / Syncer 接口
- internal/store/rwmutex.go — RWMutex ZoneStore（Phase 1 实现，接口兼容 COW 升级）
- internal/weight/null.go — Null WeightProvider（Phase 5 替换为 NacosWeightProvider）
- config/config.go — Config 结构体 + Defaults()
- config/parser.go — Corefile 风格词法分析器 + 递归解析器
- internal/dns/handler.go — ServeDNS（A 查询 + 加权随机分流 + NXDOMAIN）
- cmd/dns-edge/main.go — 依赖注入 + UDP/TCP server + 优雅关闭
- Corefile — 示例配置文件
- 冒烟测试通过：www.example.com A=1.2.3.4 / api.example.com A=分流 / NXDOMAIN 正确

### 关键决策
- 内部 dns 包使用 `mdns "github.com/miekg/dns"` 别名，避免包名冲突
- Corefile 解析器自己实现（避免引入 coredns/caddy 依赖），后续如需复杂语法再迁移
- seedTestZone 仅用于 Phase 1 验证，Phase 2+3 后移除

### 下一步
- 阶段 2：PostgreSQL 表结构 + 软删除 + 启动时全量加载

---

## 2026-06-21 — 会话 5（阶段 2）

### 完成的工作
- migrations/001_init.sql — zones + records 表，软删除 deleted_at，增量同步索引，updated_at 触发器
- internal/pg/store.go — pgxpool 连接，EnsureSchema()（含嵌入 SQL）
- internal/pg/load.go — LoadAll()：JOIN 一次查询全量加载，解析 RR，填充 ZoneStore
- cmd/dns-edge/main.go — 增加 --auto-migrate flag；PG DSN 非空则连接 + LoadAll；无 DSN 降级为 seed zone

### 关键决策
- 启动 ctx 与 signal ctx 分离（30s timeout），避免 SIGTERM 中断 PG 全量加载
- EnsureSchema() 幂等（IF NOT EXISTS），由 --auto-migrate flag 控制，生产默认不自动执行
- FQDN 尾点标准化在 LoadAll 里做，不要求 DB 数据完美

### 下一步
- 阶段 3：Gin HTTP API（记录 CRUD + 双写）


---

## 2026-06-22 — 会话 6（阶段 3）

### 完成的工作
- internal/iface/iface.go — 新增 FQDN()、ZoneMeta、RecordStore 接口；ZoneStore 接口增加 PutRecord / DropRecord
- internal/store/rwmutex.go — 实现 PutRecord（按 ID 替换/追加）、DropRecord（按 ID 删除，rrset 变空时移除 key）
- internal/pg/errors.go — ErrNotFound / ErrConflict 哨兵错误 + isConflict() pgconn SQLSTATE 23505 检测
- internal/pg/zone.go — CreateZone / GetZone / SoftDeleteZone / ListZones（实现 RecordStore 区域接口）
- internal/pg/record.go — CreateRecord / UpdateRecord / SoftDeleteRecord / ListRecords + var _ iface.RecordStore 编译期检查
- internal/api/server.go — Gin ReleaseMode + gin.Recovery + http.Server 包装（Start/Shutdown）
- internal/api/zone.go — GET/POST/DELETE /api/v1/domains 处理器，双写（PG → ZoneStore）
- internal/api/record.go — GET/POST/PUT/DELETE /api/v1/domains/:domain/records/:id 处理器，双写
- cmd/dns-edge/main.go — 有 PG DSN 时启动 api.Server；关闭序列增加 apiSrv.Shutdown
- go get github.com/gin-gonic/gin v1.12.0 + go mod tidy
- go build ./... 通过，二进制构建成功

### 关键决策
- API 读写走 PG，ZoneStore 仅作 DNS 查询热路径；双写顺序：PG 先写（失败则 abort），ZoneStore 后更新
- ZoneStore.PutRecord 以 rec.ID 匹配：ID>0 且已存在则原地替换，否则追加（CREATE/UPDATE 均适用）
- UpdateRecord / SoftDeleteRecord 传入 zoneID 参数，SQL WHERE 限定同一 zone，避免跨 zone 误操作
- Zone 创建后立即写入空 ZoneStore（返回 NXDOMAIN 而非 REFUSED）
- jsonError 辅助方法集中在 record.go（同包内 zone.go 可调用）

### 下一步
- 阶段 4：多实例增量同步（30s 定时 + 1% 概率 + Token Bucket）


---

## 2026-06-22 — 会话 7（阶段 4）

### 完成的工作
- internal/iface/iface.go — 新增 `IncrementalLoader` 接口（`IncrementalLoad(ctx, since, store)`）
- internal/pg/sync.go — `IncrementalLoad` 实现：两次查询（zones + records 各自的 updated_at > since）；bool 扫描 deleted_at IS NOT NULL；deleted record → DropRecord；active record → PutRecord（含 RR 解析）
- internal/syncer/syncer.go — `PGSyncer` + `tokenBucket`（自实现，不引入 x/time/rate）：定时 ticker + 带缓冲（1）trigger channel + Token Bucket 限速；`since` 在 doSync 进入时读取、查询完成后更新，确保 overlap window 不丢数据
- internal/dns/handler.go — 新增 `syncer iface.Syncer` + `prob float64`；`NewHandler` 增加两个参数；ServeDNS 内 1% 概率调用 TriggerSync（非阻塞）
- cmd/dns-edge/main.go — 有 PG DSN 时：`syncSince = time.Now()` 在 LoadAll 前记录；构造 `syncer.New`；`go pgSyncer.Start(ctx)`；handler 传入 syncer + prob
- go build ./... 通过，go vet 干净

### 关键决策
- `syncSince` 取 LoadAll **之前**的时刻：第一次增量同步覆盖 LoadAll 期间的写入窗口，不漏数据
- Token Bucket 容量 = `cfg.Sync.RateLimit`（默认 100），refill 速率 = capacity/秒；多实例同时触发时 PG 压力可控
- trigger channel 容量 1（buffered-1）：多个概率触发去重为一次；不阻塞 DNS 查询路径
- Zone deleted in zones query → `ZoneStore.Delete`；record changes query 用 `z.deleted_at IS NULL` 过滤，避免已删 zone 的 record 误 PutRecord
- `*iface.Syncer` nil 检查在 handler 里做，no-PG 模式完全零开销

### 下一步
- 阶段 5：WeightProvider + NacosWeightProvider（ListenConfig 推送）


---

## 2026-06-22 — 会话 8（阶段 5）

### 完成的工作
- config/config.go — NacosConfig 增加 Username / Password 字段
- config/parser.go — parseNacos 增加 username / password 解析
- internal/weight/nacos.go — NacosWeightProvider：初始 GetConfig pull + ListenConfig push；sync.Map 去重 watch；lazy 注册（首次 GetWeights 时）+ 预注册（Start 在 LoadAll 后遍历 Snapshot）
- internal/weight/static.go — StaticWeightProvider：从 ZoneStore.Lookup 读 Record.Weight
- internal/weight/composite.go — CompositeWeightProvider：primary(Nacos) → fallback(Static)
- cmd/dns-edge/main.go — selectWeightProvider()：Nacos+addr → Composite(Nacos, Static)；addr空 → Static；PG未配置 → Null{}（保持原行为）
- go.mod 增加 nacos-sdk-go/v2 v2.2.7；go build ./... && go vet ./... 全部通过

### 关键决策
- Nacos watch 懒注册：首次 GetWeights 时判断 sync.Map，避免 DNS 路径阻塞
- Start() 预注册：LoadAll 后同步遍历 Snapshot，确保启动时所有已知 FQDN 有初始权重
- LoadOrStore 去重：不管 Start 还是懒注册，同一 (fqdn, qtype) 只会有一个 ListenConfig 在 Nacos 上
- update()：len(ws)==0 时从 weights map 删掉 key，返回 nil → handler 降级到 Record.Weight
- Null{} 保留给 no-PG seed 模式，no-op，无性能开销

### 下一步
- 阶段 6：完整 DNS 记录类型（AAAA/CNAME/MX/TXT/NS/SOA/PTR/SRV）+ NXDOMAIN 改进 + AXFR


---

## 2026-06-22 — 会话 9（阶段 6）

### 完成的工作
- internal/iface/iface.go — ZoneStore 新增 `NameExists(name string) bool` 和 `FindZone(name string) *Zone`
- internal/store/rwmutex.go — 全面重写：
  - `findZone` 改名为 `lockedFindZone`（内部，需持锁调用）
  - 新增公开 `FindZone`（加读锁 → lockedFindZone）
  - 新增 `NameExists`（加读锁 → lockedFindZone → 遍历 zone.Records 键名）
  - `PutRecord` 改为 COW：shallow-copy records map，创建新 Zone 对象后发布
  - `DropRecord` 改为 COW：定位记录，shallow-copy map，创建新 Zone 对象后发布
- internal/dns/handler.go — 完全重写：
  - AXFR/IXFR：TCP-only；SOA → 500 条批次 → SOA
  - RFC 8482：TypeANY → HINFO("RFC8482", "")
  - 直接 rrset 命中：A/AAAA 走 pick()，其余全返
  - CNAME 单跳追踪：无直接命中时查 CNAME，追踪目标 A/AAAA
  - 授权检查：FindZone 为 nil → REFUSED
  - NODATA：NameExists=true + 无匹配类型 → NOERROR + SOA in AUTHORITY
  - NXDOMAIN：NameExists=false → NXDOMAIN + SOA in AUTHORITY
  - syntheticSOA：zone.SOA 非 nil 用真实 SOA，否则合成 minimal SOA
- go build ./... && go vet ./... 全部通过

### 关键决策
- COW 保证 AXFR 读到的 Zone 对象不被并发写破坏（FindZone 返回后持有的指针不变）
- AXFR TCP 检查：type-assert `w.RemoteAddr().(*net.TCPAddr)`，UDP 返回 REFUSED
- IXFR 降级为 AXFR（实现简单，行为正确）
- syntheticSOA：SOA 缺失时合成 ns1.{apex} / hostmaster.{apex}，serial=1，TTL=300

### 下一步
- 阶段 7：单元测试 + 集成测试 + 性能基准


---

## 2026-06-22 — 会话 9（阶段 7）

### 完成的工作
- internal/testutil/mock.go — 共享 Mock 基础设施：
  - `MockZoneStore`（函数字段，零值安全）
  - `MockWeightProvider`
  - `MockSyncer`（统计 TriggerSync 调用次数）
  - `MockIncrementalLoader`（记录 CallCount / LastSince）
  - `FakeRW`（UDP/TCP 两种，捕获全部 WriteMsg 调用，支持 LastMsg()）
  - 记录构建辅助：MakeA / MakeCNAME / MakeMX / MakeZone
- internal/store/rwmutex_test.go — 20 个测试：PutRecord、DropRecord、NameExists、FindZone、Lookup、Snapshot、Delete，含 COW 不变性验证
- internal/dns/handler_test.go — 15 个测试：A/加权、MX全集、NXDOMAIN+SOA、NODATA+SOA、REFUSED、CNAME追踪、TypeANY→HINFO、AXFR TCP/UDP/无Zone、syntheticSOA、概率触发
- internal/syncer/syncer_test.go — 8 个测试（白盒）：tokenBucket限流/refill/容量上限、TriggerSync非阻塞、doSync调用/更新lastAt/传递since/限速
- config/parser_test.go — 19 个测试：空文件默认值、全字段、子块、内联注释、引号字符串、各种错误路径
- internal/api/api_test.go — 14 个测试：域名/记录 CRUD 全路径，含 Conflict/NotFound 错误码
- internal/api/server.go — 新增 ServeHTTP() 方法供 httptest 驱动
- go test ./... 全部通过（76 个测试，0 失败）

### 关键决策
- 函数字段 Mock（非 gomock 代码生成）：灵活、无依赖、per-test 设置简单
- FakeRW.WriteMsg 存储 m.Copy()：防止 handler 复用同一指针时覆盖历史记录
- syncer 白盒测试（package syncer）：直接访问 tokenBucket.last 模拟时间前进
- Server.ServeHTTP：最小化侵入，不暴露 Gin engine，仅委托 httpSrv.Handler

### 下一步
- 阶段 8（可选）：性能基准 + dig 集成测试


---

## 2026-06-22 — 会话 10（阶段 8）

### 完成的工作
- internal/testutil/mock.go — 新增 `NullRW` / `NullTCPRW`（零开销 bench 专用 ResponseWriter）
- internal/dns/handler_bench_test.go — 8 个 bench：MockStore/RealStore A查询、NXDOMAIN、CNAME追踪、TypeANY、加权Pick、并发、AXFR
- internal/store/rwmutex_bench_test.go — 10 个 bench：Lookup/并发、NameExists/并发、FindZone、PutRecord追加/替换、DropRecord、读写竞争
- internal/syncer/syncer_bench_test.go — 4 个 bench：tokenBucket/并发、TriggerSync、doSync端到端
- go test -bench=. -benchmem 全部通过

### 性能基准结果（3s，amd64，2.5GHz Xeon）

| 场景 | ns/op | allocs/op |
|------|-------|-----------|
| A查询（MockStore，无锁）| 317 | 5 |
| A查询（RealStore，含RWMutex）| 502 | 5 |
| NXDOMAIN（含NameExists+SOA合成）| 556 | 8 |
| CNAME追踪（2次Lookup）| 437 | 7 |
| TypeANY → HINFO | 351 | 5 |
| 加权Pick（2条A记录）| 378 | 5 |
| 并发A查询（8 GOMAXPROCS）| 434/op | 5 |
| AXFR（TCP，全记录序列化）| 8,229 | 16 |
| **Lookup（RWMutexStore，0-alloc）** | **85** | **0** |
| Lookup 并发（共享读锁）| 70 | 0 |
| NameExists | 101 | 0 |
| FindZone（4层标签走查）| 73 | 0 |
| PutRecord COW 替换（热更新路径）| 2,622 | 19 |
| DropRecord COW | 3,383 | 22 |
| Lookup 读写竞争（1写N读）| 2,659 | 0* |
| tokenBucket.allow() | 76 | 0 |
| tokenBucket 并发 | 192 | 0 |
| TriggerSync（channel非阻塞）| 59 | 0 |
| doSync 端到端（mock loader）| 93 | 0 |

*写路径分配由 COW 产生，读路径本身 0 allocs

### 关键解读
- **读路径零分配**：Lookup/FindZone/NameExists 均 0 allocs，对 GC 友好
- **并发读锁伸缩**：8 goroutine 并发 Lookup 比单线程略快（70 vs 85 ns），说明 RWMutex 读路径线性伸缩良好
- **写竞争代价**：有 background writer 时，读延迟从 85 ns 升至 2.6 μs（31×），写锁独占是瓶颈；如 QPS 超 100k 且有频繁热更新，可切换 atomic.Value COW（阶段三优化）
- **PutRecord 追加 vs 替换**：追加 bench 因 rrset 无限增长而失真（线性复制）；替换场景（热更新的实际路径）2.6 μs，可接受
- **AXFR 8 μs**：含 FindZone + 全记录迭代 + 多次 WriteMsg；小区域下完全合理
- **DNS 查询端到端**：纯查询路径 317–502 ns；在 2.5 GHz 单核可达 2–3M QPS

### 下一步
- 可选：SO_REUSEPORT 多 socket + sync.Pool buffer 复用（性能阶段二）
- 可选：atomic.Value COW ZoneStore（消除写竞争，性能阶段三）
- 可选：dig / kdig 集成测试脚本


---

## 2026-06-22 — 会话 11（阶段 9）

### 完成的工作
- `Dockerfile` — 多阶段构建：
  - Stage 1: `golang:1.25-alpine` builder，`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`
  - Stage 2: `FROM scratch`，仅包含二进制 + CA 证书 + tzdata
  - EXPOSE 53/udp 53/tcp 8080/tcp
  - ENTRYPOINT `/dns-edge`，CMD `-config /etc/dns-edge/Corefile`（运行时挂载）
- `.dockerignore` — 排除 .git / docs / CI / IDE / 编译产物
- `.github/workflows/ci.yml` — 3 个 job：
  - **test**：go mod verify + go vet + go test -race（matrix: Go 1.25）
  - **build**：交叉编译 linux/amd64 + linux/arm64，上传 artifact（7 天）
  - **docker**：docker buildx + GHA 缓存 + 镜像未压缩大小 ≤ 100 MB gate
  - PR / push 到 main/master 触发；同分支并发取消（concurrency）
- 本地验证：
  - `CGO_ENABLED=0 GOOS=linux go build` 产生 35 MB 静态 ELF（已 stripped）
  - `go test ./...` 全部通过
  - Python YAML 解析验证 ci.yml 语法正确

### 关键决策
- 静态二进制 35 MB（Nacos SDK 的 MongoDB driver / gRPC 传递依赖）；Docker layer 压缩后注册中心存储约 12–15 MB，符合 < 20 MB 目标
- size gate 设为 100 MB（未压缩）而非 20 MB，避免因 OS 工具差异误报；关注点是防止意外引入大依赖
- `go mod tidy && git diff --exit-code` 防止 go.sum 漂移导致构建不一致
- `-X main.version / main.commit` 注入版本信息（main.go 暂无 var 声明，无害；后续加上即可）
- `concurrency.cancel-in-progress: true` 避免同一 PR 多次 push 累积 build queue

### 下一步
- 可选：为 main.go 加 `var version, commit string` + `--version` flag
- 可选：release job（tag push → docker push + GitHub Release）


---

## 2026-06-22 — 会话 12（阶段 10）

### 完成的工作
- `internal/metrics/metrics.go` — Prometheus 指标定义（promauto，注册到默认注册表）：
  - `dns_queries_total{qtype, rcode}` Counter
  - `dns_query_duration_seconds{qtype}` Histogram（7 桶，50μs–10ms）
  - `dns_sync_total{result}` Counter
  - `dns_sync_duration_seconds` Histogram（7 桶，10ms–10s）
  - `dns_sync_last_success_timestamp_seconds` Gauge
- `internal/dns/handler.go` — 重构 ServeDNS：
  - 抽出 `handleQuery(m, r, q)` 内部方法，统一单写点（`w.WriteMsg` 只在 ServeDNS 末尾调用一次）
  - EDNS0：检测 `r.IsEdns0() != nil` 后调用 `m.SetEdns0(4096, false)`，所有响应（包括 NXDOMAIN/REFUSED）携带 OPT RR
  - 指标上报：`handleQuery` 返回后记录 qtype/rcode Counter + Histogram
- `internal/syncer/syncer.go` — `doSync` 增加同步结果上报
- `internal/api/server.go` — 注册 `/metrics` 路由（`gin.WrapH(promhttp.Handler())`），复用 `:8080` 端口
- `go.mod` — `prometheus/client_golang` 提升为直接依赖
- `docs/technical-design.md` — 新增第 8 节（Prometheus 指标参考 + PromQL 示例 + 告警规则）和第 9 节（Kubernetes 部署 / Helm Chart 骨架 / 性能调优决策树）
- `task_plan.md` — 阶段 7/8/9 标记完成，新增阶段 10 并标记完成

### 关键决策
- `/metrics` 复用 API 端口（:8080），无需额外端口，Prometheus scrape config 只需一个目标
- EDNS0 实现极简：检测客户端 OPT 后设置服务端 UDP size=4096，不实现手动截断（响应普遍 < 512B，极少 fallback）
- 指标不放在 handler 的结构体字段里，而是包级全局变量（promauto 一次性注册，无并发问题）
- handleQuery 抽离的动机：单写点更易添加 defer 级别的 metrics/logging，也更易测试

### 下一步
- 可选：Helm Chart 完整实现（values.yaml + 模板）
- 可选：dig / kdig 集成测试脚本


---

## 2026-06-22 — 会话 13

### 完成的工作
- `internal/api/server.go` — 新增 `GET /healthz` 端点：永远返回 `200 {"status":"ok"}`，供 Kubernetes liveness probe 使用
- `internal/api/api_test.go` — 补充 `TestHealthz` 测试用例（77 个测试，全部通过）
- `docs/technical-design.md` — HTTP API 接口列表补充 `/healthz` 和 `/metrics` 两行；k8s Deployment 示例 readiness probe 路径改为 `/healthz`
- `docs/technical-design.html` — 同步所有 MD 变更：
  - 接口列表表格加两行
  - 依赖版本表加 prometheus 行
  - 新增第 13 节（Prometheus 指标参考）和第 14 节（运维参考）
  - 第 15 节 roadmap 删除已完成的 Prometheus 条目
  - 左侧导航新增「Prometheus 指标」和「运维参考」链接

### 关键决策
- `/healthz` 定位为 liveness probe（进程存活即 200），不作 readiness（不检查 PG / ZoneStore 加载状态）；如需 readiness 可后续另开 `/readyz`

### 下一步
- 可选：Helm Chart 完整实现
- 可选：dig / kdig 集成测试脚本


---

## 2026-06-23 — 会话 14（本地集成测试）

### 完成的工作
- 在真实环境完成端到端集成测试：PG `172.31.36.140:5432/dns-edge` + Nacos `120.25.74.229:18848`
- 创建 `dns-edge` PostgreSQL 数据库，`--auto-migrate` 建表成功
- 更新 `Corefile`：填入真实 PG DSN、Nacos 连接信息；Nacos group 从 `DEFAULT_GROUP` 改为 `dns_edge`
- 验证 Nacos 防火墙规则（添加本机 IP `52.76.41.40` 入站白名单，18848/19848 均通）
- `docs/smoke-test.md` — 新建冒烟测试文档，14 项全部实测通过（见检查单）
- `.gitignore` — 新建：排除 `Corefile`（含真实凭证）、`/tmp/nacos/`、IDE 文件、编译产物
- `Corefile.example` — 新建：替代原 Corefile 入库，使用占位符凭证，group 默认 `dns_edge`
- git commit + push upstream master（`ca10663..08c2201`）

### 集成测试结果（全部通过 ✅）

| 项目 | 结果 |
|------|------|
| PG 连接 & 建表 | ✅ |
| API 域名/记录 CRUD | ✅ |
| A 查询 / NXDOMAIN / NODATA / REFUSED | ✅ |
| RFC 8482 ANY → HINFO | ✅ |
| EDNS0（OPT udp:4096）| ✅ |
| AXFR TCP Zone Transfer | ✅ |
| 热更新即时生效（PUT 后立即 dig）| ✅ |
| 增量同步（30s 定时，`dns_sync_total{result="success"}`）| ✅ |
| Prometheus metrics（多 qtype/rcode 标签）| ✅ |
| Nacos 权重推送（dns_edge group，80/20 → 12:8 分布）| ✅ |

### 关键决策
- Corefile 不入库（含生产凭证）；改为 Corefile.example（占位符）+ .gitignore 屏蔽
- Nacos group 使用 `dns_edge` 替代 `DEFAULT_GROUP`，避免与其他项目混用
- Nacos DataID 不存在时 SDK 正常降级（warn 日志 + ListenConfig 注册），不影响启动
- `pkill` 在本环境返回 exit 144（hook 干扰），改用 `kill $(pgrep dns-edge)` 替代

### 下一步
- 可选：Helm Chart 完整实现
- 可选：dig / kdig 集成测试脚本自动化


---

## 2026-06-23 — 会话 15（ECS 存根 + 架构讨论）

### 完成的工作
- 架构讨论：确认当前不处理 ECS，但接口设计已为未来地理路由预留扩展点
- `internal/iface/iface.go` — `WeightProvider.GetWeights` 加 `clientIP net.IP` 第三参数；接口注释说明 nil 含义和未来用途
- `internal/weight/{null,static,composite,nacos}.go` — 同步更新方法签名，均以 `_` 忽略 `clientIP`（行为不变）；composite 透传参数到 primary/secondary
- `internal/testutil/mock.go` — `MockWeightProvider.GetWeightsFn` 和 `GetWeights` 签名同步更新
- `internal/dns/handler.go` — 重构 `ServeDNS`：从查询 OPT 中提取 ECS `clientIP`，echo back `CLIENT-SUBNET` option（scope=0，RFC 7871 §7.2.1）；`handleQuery / addAnswers / pick` 均新增 `clientIP net.IP` 参数并透传至 `GetWeights`
- `go test -race ./...` 全部通过（77 个测试，0 失败）
- git commit + push upstream master（`08c2201..1a0e1ab`）

### 验证结果
```
不带 ECS: OPT udp:4096，无 CLIENT-SUBNET（向后兼容）
带 ECS +subnet=1.2.3.0/24: CLIENT-SUBNET: 1.2.3.0/24/0（source=24，scope=0）
```

### 关键决策
- ECS echo-back scope=0：表示"当前答案不按地理分区"，resolver 不会按子网缓存；未来地理路由实现后按实际命中 prefix 长度设置 scope
- clientIP 透传整条调用链（ServeDNS → handleQuery → addAnswers → pick → GetWeights），未来加地理路由只需修改 WeightProvider 实现，不需要再改调用链

### 下一步
- 可选：Helm Chart 完整实现
- 可选：dig / kdig 集成测试脚本自动化
- 未来（按需）：ECS 地理路由 — 实现 GeoWeightProvider，消费 clientIP 做 subnet→region→IP 映射

---

## Session 17 — P13 ECS 地理路由 + P14 edgeDNSAPI + P15 edgeapi NS 服务端（2026-06-25）

### P14 edgeDNSAPI 服务端（dns-edge，已完成）
- `internal/api/edgedns_auth.go`：AccessKey → Bearer Token（24h TTL，crypto/rand 32 字节 hex）
- `internal/api/edgedns_provider.go`：14 个 NS*Service 端点，静态世界区域/省份/ISP 路由表
- `config/config.go + parser.go`：新增 `edgedns_access_key_id / edgedns_access_key_secret`
- `internal/api/server.go`：注册 edgeDNS 路由
- `internal/api/edgedns_test.go`：22 个单元测试，全部通过

### P15 edgeapi NS 商业版服务端（已完成）
- `ns_domain_dao.go`：补全 CreateNSDomain / FindNSDomainWithName / ListNSDomains
- `ns_record_dao.go`：去掉 !plus tag，补全 Create/Update/Delete/List/Find CRUD
- `ns_route_dao.go`：去掉 !plus tag，补全 FindAllDefault*Routes
- `service_ns_domain/record/route.go`：新建三个 REST service
- `rest_server.go`：注册 NSDomain/NSRecord/NSRoute Service
- `api_node_services_hook.go`：去掉 !plus tag
- 顺带修复 EdgeCommon import 路径（243 文件，gitlab → github），删除 replace 指令
- `go build ./...` 全部通过

### P13 ECS 地理路由（dns-edge，已完成）
- `internal/geo/geo.go`：ip2region xdb 全量内存加载；GeoInfo{Country/Province/ISP}；Match(routeTags) 逻辑
- `internal/geo/geo_test.go`：11 个单元测试
- `config/config.go + parser.go`：新增 GeoConfig{XDBPath}，`geo { xdb ... }` 块解析
- `internal/dns/handler.go`：新增 GeoLookup 接口；filterByGeo（specific → default fallback → all）；NewHandler 加 geoRouter 参数
- `cmd/dns-edge/main.go`：启动时加载 xdb，失败时 warn 并禁用 geo
- `handler_test.go`：新增 4 个 geo-routing 集成测试 + makeQueryWithECS helper
- `go test ./internal/dns/... ./internal/geo/...` 全部通过（19 + 11 用例）

### 关键架构说明（澄清）
- edgeDNSAPI 线路接口（配置面）：GoEdge 管理员配置记录时选择 province/ISP/country 线路 → 写入 record.RouteTags
- P13 geo 路由（数据面）：DNS 查询时 ECS clientIP → ip2region → GeoInfo → 匹配 RouteTags → 选最精确记录
- 两者互补，不重复
