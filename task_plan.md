# 任务计划

## 项目概述
**项目名称**: dns-edge — 权威 DNS 服务（带热更新 API）
**目标**: 实现一个权威 DNS 服务器，支持通过 HTTP API 对 DNS 记录/路由进行热更新，无需重启服务
**当前状态**: 阶段 0 完成，进入阶段 1（项目初始化）

## 技术选型（已确认）

| 组件 | 选型 | 说明 |
|------|------|------|
| DNS 协议层 | `miekg/dns` | Go 原生，单二进制，无外部依赖 |
| HTTP API | `gin` | 热更新接口 |
| 持久化 | PostgreSQL | DNS 记录双写，source of truth |
| 分流权重配置 | Nacos | 替代 Redis；ListenConfig 推送，毫秒级生效 |
| DoT/TLS | dnsdist 前置 | 权威 DNS 不处理 TLS |
| 配置格式 | Corefile 风格（coredns/caddy 解析） | 与现有 coredns-ecs 项目保持一致 |
| 容器化 | FROM scratch | 目标镜像 < 20MB |
| 日志 | go.uber.org/zap | 结构化日志 |
| 指标 | prometheus/client_golang | Metrics 暴露 |

> **说明**：dns-edge 是 miekg/dns 项目，不是 CoreDNS 项目。不使用 coremain/plugin chain/内置插件，仅借鉴 Corefile 配置风格和插件结构模式。

## 架构决策（已确认）

- **热更新**：API 写 PG + 更新本实例内存（双写，PG 失败则 abort）
- **多实例同步**：定时轮询（30s）+ 概率触发（1%）增量拉取 PG
- **分流**：DNS 层读内存 weightMap（Nacos ListenConfig 回调更新）→ 加权随机选 IP；采样系统写 Nacos DataID
- **DNSSEC**：不实现，由 dnsdist 层处理
- **AXFR**：需要实现，支持多实例主从同步
- **持久化软删除**：records 表用 `deleted_at`，确保增量拉取能感知删除

## 设计原则（已确认）

- 模块间只依赖 interface，具体实现在 `cmd/dns-edge/main.go` 注入
- ZoneStore 第一阶段用 RWMutex，压测后可无缝换 COW（atomic.Value）
- WeightProvider 接口化，实现：NacosWeightProvider（主）/ StaticWeightProvider（降级，读 PG 静态权重）/ CompositeWeightProvider（Nacos 不可用时自动降级）
- 配置采用 Corefile 风格块级语法，`workers` 字段控制 SO_REUSEPORT 数量（默认 0 禁用）

## 性能优化策略（分阶段启用）

| 阶段 | 优化项 | 触发条件 |
|------|--------|----------|
| 一（默认） | RWMutex + 单 socket | 开发期 |
| 二 | SO_REUSEPORT + sync.Pool + EDNS0 | 压测 QPS 瓶颈 |
| 三 | atomic.Value COW | GC / 锁竞争明显 |

## 阶段规划

### 阶段 0：技术选型决策 ✅ 完成

### 阶段 1：项目初始化 ✅ 完成
- [x] `go mod init`，目录结构搭建
- [x] 定义核心 interface（ZoneStore / WeightProvider / Syncer）
- [x] 最小可运行 DNS server（能响应 A 记录查询）
- [x] Corefile 风格配置解析

### 阶段 2：存储层 ✅ 完成
- [x] ZoneStore RWMutex 实现
- [x] PostgreSQL 表结构 + 软删除（deleted_at + updated_at 触发器）
- [x] 启动时从 PG 全量加载（LoadAll）
- [x] 明确功能需求
- [x] 方案对比（PowerDNS+Lua vs miekg/dns）
- [x] 确定最终选型（miekg/dns）
- [x] 架构设计（dnsdist + 多实例 + Redis/PG）
- [x] 编写 README.md、技术文档（MD + HTML）

### 阶段 1：项目初始化（当前）
- [ ] `go mod init`，目录结构搭建
- [ ] 定义核心 interface（ZoneStore / WeightProvider / Syncer）
- [ ] 最小可运行 DNS server（能响应 A 记录查询）
- [ ] Corefile 风格配置解析

### 阶段 2：存储层
- [ ] ZoneStore RWMutex 实现
- [ ] PostgreSQL 表结构 + 软删除
- [ ] 启动时从 PG 全量加载

### 阶段 3：热更新 API ✅ 完成
- [x] Gin HTTP 服务集成（ReleaseMode + Recovery + http.Server 包装）
- [x] 记录 CRUD 接口（7 个端点）
- [x] 双写逻辑（PG 先写，ZoneStore 后更新）
- [x] RecordStore 接口 + pg 实现（zone.go + record.go）
- [x] ZoneStore.PutRecord / DropRecord（按 ID 精确更新）

### 阶段 4：多实例同步 ✅ 完成
- [x] 定时增量拉取（30s ticker）
- [x] 概率触发同步（Prob=1%，DNS 查询路径非阻塞）
- [x] Token Bucket 限速（capacity = RateLimit，refill = RateLimit/s）
- [x] IncrementalLoad：zone 删除 + record upsert/delete 两次查询

### 阶段 5：分流权重 ✅ 完成
- [x] NacosWeightProvider（GetConfig 初始拉取 + ListenConfig 推送）
- [x] StaticWeightProvider（从 ZoneStore Record.Weight 读取）
- [x] CompositeWeightProvider（Nacos → Static 降级）
- [x] Start() 预注册 + GetWeights() 懒注册（sync.Map 去重）
- [x] config 增加 username/password 字段

### 阶段 6：标准功能完善 ✅ 完成
- [x] 完整记录类型（AAAA/CNAME/MX/TXT/NS/SOA/PTR/SRV）
- [x] NXDOMAIN / NOERROR 负向应答（NameExists 区分 NODATA vs NXDOMAIN）
- [x] SOA 在 AUTHORITY 段（syntheticSOA 兜底）
- [x] CNAME 单跳追踪
- [x] AXFR Zone Transfer（TCP-only，SOA→记录500批→SOA）
- [x] RFC 8482 TypeANY → HINFO
- [x] ZoneStore COW（PutRecord/DropRecord 发布新 Zone 对象，不可变）

### 阶段 7：单元测试 ✅ 完成
- [x] MockZoneStore / MockWeightProvider / FakeRW / NullRW 共享 mock 基础设施
- [x] internal/store/rwmutex_test.go — 20 个测试，含 COW 不变性验证
- [x] internal/dns/handler_test.go — 15 个测试，含 AXFR/CNAME/TypeANY/NODATA
- [x] internal/syncer/syncer_test.go — 8 个白盒测试，含 tokenBucket 时间前进
- [x] config/parser_test.go — 19 个测试
- [x] internal/api/api_test.go — 14 个测试

### 阶段 8：性能基准测试 ✅ 完成
- [x] internal/dns/handler_bench_test.go — 8 个基准（MockStore / RealStore / 并发 / AXFR）
- [x] internal/store/rwmutex_bench_test.go — 10 个基准（含读写竞争场景）
- [x] internal/syncer/syncer_bench_test.go — 4 个基准
- [x] 关键结果：A查询 ~500 ns/op，Lookup 纯读路径 ~85 ns/0 allocs

### 阶段 9：容器化与 CI ✅ 完成
- [x] Dockerfile — 多阶段 FROM scratch，CGO_ENABLED=0，~35MB 静态二进制
- [x] .dockerignore — 排除 .git / docs / CI 文件
- [x] .github/workflows/ci.yml — test（race）+ build（amd64/arm64）+ docker（size gate 100MB）

### 阶段 10：可观测性与协议扩展 ✅ 完成
- [x] Prometheus 指标（`internal/metrics/metrics.go`）
  - `dns_queries_total{qtype, rcode}` — Counter
  - `dns_query_duration_seconds{qtype}` — Histogram（7 桶）
  - `dns_sync_total{result}` — Counter
  - `dns_sync_duration_seconds` — Histogram
  - `dns_sync_last_success_timestamp_seconds` — Gauge
- [x] `/metrics` 端点（`internal/api/server.go` 注册 `promhttp.Handler()`，复用 :8080 端口）
- [x] `/healthz` 端点（liveness probe，永远返回 `200 {"status":"ok"}`）
- [x] EDNS0 支持（handler.go：`r.IsEdns0() != nil → m.SetEdns0(4096, false)`，所有响应含 OPT RR）
- [x] DNS handler 重构为单写点（handleQuery 只填充 m，ServeDNS 统一 WriteMsg + 计时）
- [x] Syncer 同步结果上报（success / error counter + duration + last_success gauge）
- [x] 技术文档更新（MD + HTML，第 8 节 Prometheus 指标参考 + 第 9/14 节运维参考）

## 关键决策记录

| 日期 | 决策 | 理由 |
|------|------|------|
| 2026-06-20 | 选型 miekg/dns，放弃 PowerDNS+Lua | Go 全栈，容器化友好，环境已有 Go 1.25 |
| 2026-06-21 | PG + 内存双写，增量同步最终一致 | 避免强一致带来的复杂度，DNS TTL 可容忍短暂不一致 |
| 2026-06-21 | Redis 存分流权重，采样系统解耦 | DNS 查询路径无 I/O，采样系统独立迭代 |
| 2026-06-21 | SO_REUSEPORT 默认禁用（workers=0） | 避免过早优化，接口设计保留开启路径 |
| 2026-06-21 | 分流权重从 Redis 改为 Nacos | 已有 Nacos 基础设施；ListenConfig Push 比 Redis 定时拉取延迟更低；少维护一个组件 |
| 2026-06-21 | 定性为 miekg/dns 项目而非 CoreDNS 项目 | CoreDNS 的 plugin chain / coremain 不适合权威 DNS 场景；仅借鉴 Corefile 风格和结构 |
| 2026-06-24 | 移除 PG/Nacos 直连，改为通过 GoEdge EdgeAPI 获取数据 | 需部署在全球边缘节点，无法直连中心 DB；同时作为 GoEdge DNS Provider 对接 |

### 阶段 11：GoEdge Provider 改造（当前，feature/goedge-provider 分支）
- [ ] P1：`internal/edgeapi/` 客户端（Token + Zone + Record CRUD）
- [ ] P2：配置层改造（新增 `edgeapi` 块，移除 `postgres`/`nacos`）
- [ ] P3：EdgeAPI Syncer（全量同步，替换 PG Syncer）
- [ ] P4：GoEdge Provider HTTP API（`/NSDomainService/...` 等 14 个接口）
- [ ] P5：鉴权中间件（AccessKeyId/Secret 校验，Token 颁发）
- [ ] P6：集成测试 + 与 GoEdge 联调
- [x] 技术文档：docs/goedge-provider-design.md + .html
