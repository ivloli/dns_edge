# 研究与发现

## 架构（已确认）

GoEdge 是 DNS 记录的 source of truth。dns-edge 是 GoEdge 管理的边缘 DNS 节点，通过 edgeDNSAPI 接收记录推送，纯内存提供 DNS 解析。

```
EdgeAdmin → edgeapi (GoEdge) → dns-edge edgeDNSAPI → ZoneStore → DNS 解析
                  ↑
      DNSTaskExecutor（20s tick，空检测 + 推送）
```

**GoEdge 调用序列**（AddRecord 举例）：
1. `POST /NSDomainService/FindNSDomainWithName {"name":"example.com"}` → 获取 domainId
2. `POST /NSRecordService/CreateNSRecord {"nsDomainId":X,...}` → 创建记录

GoEdge 不调用 CreateNSDomain，因此 `FindNSDomainWithName` 需要 lazy-create zone。

## 自动恢复机制

dns-edge 重启后内存清空，edgeapi 通过以下机制自动恢复：

**文件**：`edgeapi/internal/tasks/dns_task_executor.go` → `resyncEmptyEdgeDNSProviders()`

**逻辑**：
1. 每 20s tick 时对所有 `edgeDNSAPI` 类型的 provider 调 `GetDomains()`
2. 如果返回空列表（dns-edge 刚重启，内存为空），立即为该 provider 的所有集群插一条 `ClusterNodesChange` task
3. `DNSTaskExecutor` 在下一轮 tick（最多再 20s）处理该 task，把集群节点 A 记录全部推到 dns-edge

**实测**：dns-edge 重启后 **5 秒内** 记录恢复。

**为什么不让 dns-edge 反向调 edgeapi**：
- edgeapi 只暴露 gRPC（:8031），需要 admin/node 身份认证
- dns-edge 没有这两种身份，引入 gRPC client + pb 依赖代价太大
- 由 edgeapi 主动感知方向更简单，且不引入新的架构依赖

## 我们实现了什么（补齐商业版）

GoEdge 商业版 `edgeDNSAPI` provider 定义了协议（client 在 `provider_edge_dns_api.go`），但没有提供 server 实现。我们实现了这个 server：

| 商业版已有 | 我们补充实现 |
|-----------|-------------|
| `EdgeDNSAPIProvider`（HTTP client） | dns-edge edgeDNSAPI server（14 个端点） |
| `DNSTaskExecutor`（推送触发器） | 无 PG 模式（ZoneStore 纯内存后端） |
| `localEdgeDNS` DNS 解析 | dns-edge DNS server（:5300，miekg/dns） |
| 地理路由框架 | ip2region 五级路由（省/ISP/国家/运营商/默认） |
| ECS 框架 | ECS client subnet pass-through stub |
| — | 重启自动恢复（空检测 + 重推，edgeapi 侧） |

## no-PG 模式运行路径

启动条件：Corefile 无 `postgres` 块，有 `api { listen ... edgedns_access_key_id ... }` 块

```
main.go:
  cfg.PG.DSN == ""  &&  cfg.API.Listen != ""
  → api.New(cfg.API, nil, zoneStore, log)  // pg=nil
  → registerEdgeDNSRoutes()  // 全部 14 个端点可用
```

`edgedns_provider.go` 中：
- Zone ID = FNV-1a hash of FQDN（稳定，重启后不变）
- Record ID = 进程内 `atomic.AddInt64(&recordIDCounter, 1)`（重启后从 1 开始）
- 所有读写直接操作 `s.store`（ZoneStore），零 PG 调用

## geo 路由实现细节

### parseRegion 字段兼容

ip2region xdb 存在多个版本，字段数不同：

| 版本 | 格式 | 字段数 |
|------|------|--------|
| 旧版（edge/static/ip2region.xdb） | `国家\|省份\|城市\|ISP` | 4 |
| v3.x 新版（GitHub Releases） | `国家\|省份\|城市\|ISP\|CC` | 5 |
| 部分中间版本 | `国家\|0\|省份\|城市\|ISP` | 5（区域字段为0） |

`internal/geo/geo.go` 的 `parseRegion` 按字段数 switch，均正确提取省份和 ISP。

`normalizeProvince`：TrimSuffix `省`/`市`，让「浙江省」→「浙江」和 GoEdge 路线名对齐。  
`normalizeISP`：去掉「中国」「云」前缀，让「中国电信」→「电信」。

### 五级路由优先级

```
province+ISP > province > ISP > country > default > all records
```

**ECS 验证结果**（2026-06-27）：

| IP | 实际归属 | 命中规则 | 返回 |
|----|---------|---------|------|
| 122.224.0.1 | 浙江电信 | province:浙江 + isp:电信 | 3.3.3.3 ✓ |
| 111.0.0.1 | 浙江移动 | province:浙江 | 1.1.1.1 ✓ |
| 183.232.0.1 | 广东移动 | province:广东 | 2.2.2.2 ✓ |
| 123.125.0.1 | 北京联通 | 无匹配→默认 | 9.9.9.9 ✓ |
| 无ECS | — | clientIP=nil，随机 | 任意 ✓ |

### xdb 自动更新

**文件**：`internal/geo/updater.go`

- 启动时后台 goroutine 调 `CheckAndUpdate(force=false)`：版本标记一致则跳过，否则下载
- `Start(ctx)` 按 `update_interval`（默认 24h）定期检查
- 下载：GitHub Releases API → tarball → 提取 `data/ip2region_v4.xdb` → 原子 rename → `Router.swap()` 热替换
- Corefile 配置：`auto_update true`、`update_interval 24h`、`github_token <token>`（可选）

文件：`/home/ivloli/Git_repo/edgeapi/internal/dnsclients/provider_edge_dns_api.go`

- `getToken()`：POST `/APIAccessTokenService/getAPIAccessToken` → Bearer token（24h TTL）
- 请求头：`X-Edge-Access-Token: <token>`
- 所有操作：先 `FindNSDomainWithName` 获取 domainId，再操作 Record

## 联调环境

- dns-edge：`-config Corefile.local`，监听 :5300（DNS）+ :8080（API）
- edgeapi：`/home/ivloli/Git_repo/edgeapi/build/`，GRPC :8031
- EdgeAdmin：`/home/ivloli/Git_repo/edgeadmin/build/edge-admin`，HTTP :7788
- MySQL：127.0.0.1:3306，database db_edge

## 配置文件位置

| 文件 | 说明 |
|------|------|
| `/home/ivloli/dns_dev/Corefile.local` | dns-edge 本机联调配置 |
| `/home/ivloli/Git_repo/edgeapi/configs/` + `build/configs/` | edgeapi 配置 |
| `/home/ivloli/Git_repo/edgeadmin/configs/` | edgeadmin 配置 |

edgeadmin nodeId/secret（连接 edgeapi 用）：
- nodeId: `22d93a9e9d2d73d2349dc630090379e8`
- secret: `pg05i8JWlhQ7PluJroTAjmla5fzo0Rvq`
