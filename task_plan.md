# 任务计划

## 项目概述

**项目名称**: dns-edge — GoEdge 边缘 DNS 节点  
**定位**: GoEdge（edgeapi）的边缘 DNS 服务，接收 GoEdge 通过 edgeDNSAPI 推送的记录，纯内存提供 DNS 解析  
**Source of truth**: GoEdge（edgeapi），dns-edge 无持久化状态，仅缓存

## 架构

```
EdgeAdmin → edgeapi (GRPC :8031) → dns-edge (HTTP :8080, DNS :5300)
                                         ↑
                              edgeDNSAPI (GoEdge 主动推送记录)
```

- GoEdge 是写方（CALLER），dns-edge 是服务方（SERVER）
- dns-edge 通过 ZoneStore（纯内存）提供 DNS 解析
- 重启后 GoEdge 重新推送记录恢复状态（source of truth 在 GoEdge）
- PG 模式保留：有 DSN 时开启，用于独立部署（不依赖 GoEdge）

## 技术栈

| 组件 | 选型 |
|------|------|
| DNS 协议层 | `miekg/dns` |
| HTTP API | `gin` |
| 持久化（可选） | PostgreSQL（DSN 非空时启用） |
| 权重配置（可选） | Nacos（Nacos addr 非空时启用） |
| 日志 | `go.uber.org/zap` |
| 指标 | `prometheus/client_golang` |
| 配置 | Corefile 风格 |

## 完成阶段

| 阶段 | 内容 | 状态 |
|------|------|------|
| 1 | 项目初始化、ZoneStore 接口、最小 DNS server | ✅ |
| 2 | PostgreSQL 存储层、全量加载 | ✅ |
| 3 | HTTP API（记录 CRUD，PG 双写） | ✅ |
| 4 | 多实例增量同步（30s + 1% 概率触发 + Token Bucket） | ✅ |
| 5 | Nacos 权重提供者（主/静态降级/组合） | ✅ |
| 6 | 完整记录类型、NXDOMAIN/NODATA、CNAME、AXFR、COW | ✅ |
| 7 | 单元测试 | ✅ |
| 8 | 基准测试 | ✅ |
| 9 | 容器化（FROM scratch）、CI | ✅ |
| 10 | Prometheus 指标、/healthz、EDNS0 | ✅ |
| 11 | GoEdge customHTTP provider（POST /goedge/dns） | ✅ |
| 13 | ECS 地理路由（ip2region xdb，route_tags） | ✅ |
| 14 | edgeDNSAPI 服务端（14 个 NS*Service 端点）| ✅ |
| B方案 | no-PG 模式：edgeDNSAPI 直接读写 ZoneStore | ✅ |
| 自动恢复 | edgeapi 空检测 + 重推：dns-edge 重启后 ≤20s 自动恢复 | ✅ |
| geo修复 | ip2region parseRegion 字段索引修正 + normalizeProvince/ISP | ✅ |
| geo自动更新 | GitHub Releases 定时拉取最新 xdb，热替换无重启 | ✅ |

## 当前状态（2026-06-27）

**全链路联调已通过，ECS 地理路由验证完成，xdb 自动更新已实现**：

- EdgeAdmin UI → DNS 管理 → edgeDNSAPI provider → 手动同步 → dig 返回集群节点 IP ✓
- dns-edge 重启后，edgeapi 的 `DNSTaskExecutor` 在下一个 20s tick 检测到空 domain，自动推送记录，实测 5 秒内恢复 ✓
- ECS geo 路由 5 种场景全部验证通过：省+ISP 精确 > 省 > ISP > 国家 > 默认 ✓
- ip2region xdb 启动时自动从 GitHub 拉取最新 release（v3.16.0），定时 24h 检查热更新 ✓

## 待做

| 优先级 | 任务 |
|--------|------|
| P5 | GoEdge customHTTP 联调 |
| P12 | dns-control 中心控制服务 |
| P15 | edgeapi NS 服务单元测试 |
| P16 | EdgeAdmin 前端 NS 集群管理 UI（待评估） |

## 关键决策

| 日期 | 决策 | 理由 |
|------|------|------|
| 2026-06-20 | miekg/dns，单二进制，Corefile 配置 | Go 全栈，无外部依赖 |
| 2026-06-21 | PG 双写 + 增量同步（独立部署模式） | dns-edge 可脱离 GoEdge 独立运行 |
| 2026-06-21 | Nacos 替代 Redis 存分流权重 | 已有基础设施，Push 延迟更低 |
| 2026-06-25 | **架构更正**：GoEdge 调用 dns-edge，而非反向 | dns-edge 是 GoEdge 的 DNS Provider，GoEdge 主动推送 |
| 2026-06-25 | 保留 PG 模式，新增 no-PG 模式（B方案） | edgeDNSAPI 场景下 dns-edge 不需要持久化 |
| 2026-06-26 | edgeDNSAPI 全部走 ZoneStore，不依赖 PG | GoEdge 是 source of truth，dns-edge 纯内存缓存 |
| 2026-06-26 | FindNSDomainWithName lazy-create zone | GoEdge 不调用 CreateNSDomain，需自动建 zone |
| 2026-06-26 | 自动恢复：edgeapi 空检测，不引入新依赖 | dns-edge 无法反向调 edgeapi（gRPC 认证壁垒），由 edgeapi 主动感知 |
| 2026-06-27 | geo parseRegion 对齐参考项目（/Git_repo/dns） | 字段数按实际 xdb 版本判断（4/5字段），normalizeISP 去掉「中国」「云」 |
| 2026-06-27 | ip2region xdb 自动更新（GitHub Releases） | xdb 数据库持续维护，定期拉取保证地理精度；hot-swap 无需重启 |
