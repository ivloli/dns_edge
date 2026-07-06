# 研究与发现

## 系统架构

```
EdgeAdmin → edgeapi (gRPC :8031)
                ├── edgeDNSAPI (HTTP → dns-edge :8080) → ZoneStore → DNS :5300
                │   CDN 模式：clusterChange/recordChanged/nodeChanged 推送 A/CNAME
                └── NS gRPC 服务 ← edgeagent (dns-edge 内置)
                    NS 模式：agent 10s 轮询 FindNodeTasks，处理 nsDomainChanged/nsRecordChanged
```

**统一数据库**: `db_edge`，`db_edge_comm` 已废弃  
**edgeapi 配置目录**: `/home/ivloli/configs/`（Tea.Root 规则：`filepath.Dir(filepath.Dir(exePath))`）

---

## CDN 模式

### 推送架构
GoEdge 不调用 CreateNSDomain，FindNSDomainWithName 需要 lazy-create zone。

| 商业版已有 | 我们补充实现 |
|-----------|-------------|
| EdgeDNSAPIProvider（HTTP client） | dns-edge edgeDNSAPI server（14 个端点） |
| DNSTaskExecutor（推送触发器） | no-PG 模式（ZoneStore 纯内存后端） |
| 地理路由框架 | ip2region 五级路由（省+ISP/省/ISP/国家/默认） |
| — | 重启自动恢复（空检测 + ClusterChange，edgeapi 侧） |
| — | xdb 自动更新（GitHub Releases，hot-swap） |
| — | zoneCount O(1) 空检测（/healthz） |
| — | 通配符 CNAME（edgeapi 自动推 `*`，dns-edge RFC 4592 展开） |

### 自动恢复
`dns_task_executor.go` → `resyncEmptyEdgeDNSProviders()`：每 20s tick 调 `GetZoneCount()`，若 zoneCount=0 则触发 `DNSTaskTypeClusterChange`（nodesOnly=false），A 记录和 CNAME 均恢复。≤20s 完成。

原用 `ClusterNodesChange`（nodesOnly=true），doCluster 提前 return，CNAME 永远不推送——已修复。

### geo 路由

parseRegion 字段兼容：旧版 4 字段 `国家|省份|城市|ISP`，v3.x 5 字段加 CC。  
normalizeProvince：去掉「省」「市」后缀。normalizeISP：去掉「中国」「云」前缀。

filterByGeo IP 聚合：按 `r.Value`（目标 IP）聚合，跨该 IP 所有 record 累积 matchProvince/matchISP 标志位，整个 IP 作为单元分配唯一 tier。

优先级：`province+ISP > province > ISP > country > default > all`

### 通配符 CNAME

`*` CNAME 是集群级 DNS 规则，和 EdgeAdmin 里配置的网站域名（HTTP 层）是两个独立的层。

**edgeapi 侧**（`dns_task_executor.go` doCluster）：
- `serverDNSNames = append(serverDNSNames, "*")` 防止清理
- 若 `serverRecordsMap["*"]` 不存在则 `AddRecord("*", CNAME, clusterDomain+".")`

**dns-edge 侧**（`dns/handler.go`）：`wildcardLookup` 逐级剥 label 查 `*.parent`，主路径：直接查 → CNAME chase → wildcard 直接匹配 → wildcard CNAME chase。

**Bug：`toNSRecordObj` 尾点残留**  
`r.Name = "*.fafa.com."`，剥离 `.fafa.com.` 后剩 `"*."`（多一个点），导致 add 后立刻被清理逻辑 delete。  
修复：`toNSRecordObj` 末尾追加 `strings.TrimSuffix(shortName, ".")`。

**Bug：`findClusterDNSChanges` 缺少 `*` 保护**  
EdgeAdmin "同步"按钮走 `syncClusterDNS → findClusterDNSChanges`，独立于 doCluster 的 diff 路径，同样没有 `*` 保护，点同步后 `*` 被删。  
修复：`service_dns_domain.go findClusterDNSChanges` 前加同样逻辑。

---

## NS 模式

### 架构
edgeagent 内置在 dns-edge，启动后连接 edgeapi gRPC，10s 轮询 `FindNodeTasks`，处理：
- `nsConfigChanged`：节点配置变更（ack 即可）
- `nsDomainChanged`：调 `ListNSDomainsAfterVersion` 增量同步 zone
- `nsRecordChanged`：调 `ListNSRecordsAfterVersion` 增量同步记录

### gRPC 鉴权（AES-256-CFB）
key=secret(32B), iv=uniqueId(16B)，加密 `{"type":"dns"}`，base64 编码。  
metadata: `nodeid=uniqueId`, `token=base64(encrypted)`

### 修复记录

| 问题 | 根因 | 修复位置 |
|------|------|---------|
| DNS 节点 gRPC 鉴权全部失败 | `CreateNSNode` 没调 `CreateAPIToken`，edgeAPITokens 表无记录 | `ns_node_dao.go CreateNSNode` |
| NS 节点方法被拒 | `service_ns_node.go` 用 `UserTypeNode`，`ContainsString(["node"],"dns")=false` | 改为 `ValidateNodeId(ctx, UserTypeDNS)` |
| `FindNodeTasks` 返回 nodeId=0 | `utils_ext.go` switch 无 `UserTypeDNS` 分支 | 补加 case，调 `SharedNSNodeDAO.FindEnabledNodeIdWithUniqueId` |
| applyRecord @ 记录不写入 | `iface.FQDN("@") = "@."` 匹配不到任何 zone | `agent.go`：`@` → `iface.FQDN(NsDomain.Name)` |
| applyRecord 子域名记录不写入 | `iface.FQDN("www") = "www."` 同样匹配不到 | `agent.go`：`name` → `iface.FQDN(name + "." + NsDomain.Name)` |
| NsDomain 为 nil | `convertRecordToPB` 没填充 NsDomain 字段 | `service_ns_record.go`：查 DAO 填充 `NsDomain.Name` |

### 任务展开机制
`CreateClusterTask` 创建 nodeId=0 的集群级任务，`ExtractNSClusterTask`（定时器）展开为各节点独立任务。  
之前 `ExtractNSClusterTask` 是空实现（stub），现已实现：删除同类型单节点任务 → 为每个节点 CreateNodeTask → 删除集群级聚合任务。

### 数据库现有数据（db_edge）
- NSCluster: id=1, name='local-test-cluster'
- NSNode: id=1, uniqueId='a1b2c3d4e5f64a7b8c9d0e1f2a3b4c5d', clusterId=1
- APIToken: nodeId='a1b2c3d4e5f64a7b8c9d0e1f2a3b4c5d', role='dns', state=1
- NSDomain: id=2, name='test.local', clusterId=1
- NSRecord: domainId=2, name='@', type='A', value='10.0.0.1'

### 记录同步机制核查（2026-07-03，代码复查，未改代码）

用户回忆"重连机制"和"边缘节点 CNAME/记录同步"都已做完，逐项核对 `internal/edgeagent/agent.go`：

**记录同步（含 CNAME）——已实现，机制正确**：
`applyRecord` 不区分记录类型，统一拼 `"%s %d IN %s %s"` 交给 `miekg/dns.NewRR` 解析（agent.go:264-268），CNAME 和 A/TXT/MX 走同一条路径，无需特殊分支。写入 ZoneStore 后，查询侧 `handler.go` 的 CNAME chase（139-171行，单跳 + wildcard 两条路径）对 CDN zone 和 NS zone 是同一套代码，NS 模式的 CNAME 记录能被正确 chase。**结论：机制上没问题**，但目前只有 A 记录（`test.local`/`www.test.local`）做过 dig 实测，NS 模式下的 CNAME 记录本身没有专门 dig 验证过（CDN 模式的通配符 CNAME 测试是另一套系统，不能算数）。

**重连机制（P10）——未真正实现，仍是缺口**：
`Run()`（agent.go:49-88）只在函数开头 `grpc.DialContext` 一次：
1. `grpc.DialContext` 没加 `grpc.WithBlock()`，是非阻塞的懒连接，正常情况下几乎不会返回 err（除非 endpoint 格式非法）——所以"dial failed 直接 return"这条分支在实践中很少触发，但一旦触发，`Run()` 直接退出，`main.go` 里只有一次 `go agent.Run(ctx)`（main.go:165），没有任何外层循环重启它，goroutine 死了就永久死了。
2. 之后的自动重连完全依赖 grpc-go **channel 自身**的默认行为（内置指数退避重连底层 transport）——这是 gRPC 库免费提供的，不是我们写的代码，`poll()` 里的 `FindNodeTasks` 失败也只是 `log.Warn` 后 `return`，靠外层 10s ticker 下一轮自然重试（agent.go:97-101），这部分本身没问题。
3. 但 `UpdateNSNodeStatus(isActive:true)` 只在 `Run()` 最开始发一次（agent.go:66-72），断线重连后**不会重新上报在线状态**——如果 edgeapi 一侧因为超时把节点标记离线，dns-edge 恢复通信后 EdgeAdmin 上该节点会一直显示离线，即使任务轮询/记录同步其实已经恢复正常。
4. 没有设置 gRPC keepalive 参数，断线检测依赖 TCP/gRPC 默认值，可能不够及时。
5. 没有连接状态日志（无法从日志判断当前是否处于断线重连状态）。

**结论**：task_plan.md 里 P10 标记为"—"（未做）是准确的，用户的印象和实际代码不符——建议要么补一版真正的重连（外层 supervise 循环 + 断线后重新上报 isActive + keepalive 参数），要么至少把 UpdateNSNodeStatus 改成定期心跳而不是仅启动时一次。

### P10 修复（2026-07-03，`internal/edgeagent/agent.go`）

按上面识别的4个缺口逐一修复，纯边缘节点侧改动，不涉及 edgeapi：
- `dialWithRetry`：初始连接失败改成指数退避重试（1s→30s封顶），不再是失败即让 `Run()` 永久退出。
- `grpc.WithKeepaliveParams`（Time=20s, Timeout=10s, PermitWithoutStream=true）：加快死连接探测，让 gRPC 内置重连更快触发。
- `watchConnState`：`conn.WaitForStateChange` 监听 connectivity 状态跳变，Ready↔非Ready 都打日志，重连事件不再只能从 RPC 报错里推断。
- `reportStatus` 从"启动时发一次"改成每次 10s ticker 都发（挂在 poll 同一个 ticker 上，不额外起 goroutine），断线重连后会在下一个 tick 自动重新上报在线状态；`reportStatusOnShutdown` 在 `ctx.Done()` 时用独立短超时 context 上报离线。

验证：`go build ./...`、`go vet ./internal/edgeagent/...`、`gofmt -l` 均干净；`go test ./...` 唯一失败项 `TestEdgeDNS_ListDomains` 经 `git stash` 验证是改动前就存在的既有失败，与本次改动无关。

### edgedns_test.go 大面积测试失效核查与修复（2026-07-03）

深挖 `TestEdgeDNS_ListDomains` panic 的根因，发现不是孤立问题：`edgedns_provider.go`（`/NSDomainService/*`、`/NSRecordService/*`、`/NSRouteService/*`，即 edgeDNSAPI 真正对外的协议面）里**所有**处理函数都只读写 `s.store`（`iface.ZoneStore`），完全不碰 `s.pg`（`iface.RecordStore`，测试里叫 `rs`/`mockRS`）——这是"no-PG 模式"迁移的结果，但 `edgedns_test.go` 里差不多一半的测试还在用 `mockRS.listZonesFn`/`getZoneFn`/`listRecordsFn`/`createRecordFn`/`softDeleteRecordFn` 造数据，而这些 fixture 早就没人读了，实际测试对象 `&testutil.MockZoneStore{}` 又是空的——逐个单独跑（绕开 panic 提前终止整个测试进程的问题）确认有 12 个测试因此要么 panic、要么断言失败：`ListDomains`、`ListDomains_Pagination`、`FindDomain_Found`、`FindDomain_NotFound`、`ListRecords`、`CreateRecord_DefaultRoute`、`CreateRecord_ProvinceAndISP`、`CreateRecord_CountryRoute`、`DeleteRecord_Success`、`FindRecord_RouteTagsRoundtrip`、`FindRecords_Multiple`（`CreateRecord_ZoneNotFound` 巧合地本来就该 404，侥幸没暴露）。

（注：这套 `s.pg`/`mockRS` 仍然是另一套路由——`goedge_provider.go`/`record.go`（`/api/v1/domains`、`/goedge/dns` 等，由 `api_test.go` 测试）——的正确依赖，两套 provider 并存于同一个 `*api.Server`，只是各自读写不同的后端，这次没有动它。）

**修复方式**：把 `edgedns_test.go` 里这批测试的 fixture 从 `mockRS` 换成真实的 `internal/store.RWMutexStore`（`store.New()`），用 `testutil.MakeZone`/直接构造 `*iface.Record` 预置数据，domainId/recordId 一律通过实际调用 `/NSDomainService/FindNSDomainWithName` 现取（而不是像原测试那样假设一个写死的 id），这样测试路径和真实 GoEdge 客户端的调用顺序完全一致，也不会跟内部实现（`zoneID()` 是 FNV-1a hash，不是自增 id）耦合。

**顺带修的两个语义偏差**（原测试断言的是旧行为，和当前既有设计冲突）：
- `FindNSDomainWithName` 对不存在的域名**从不返回 nil**——`edgeDNSFindDomain` 会 lazy-create 空 zone 后返回它（这是 2026-06-26 就定下的设计决策，"GoEdge 不调用 CreateNSDomain"）。原测试 `FindDomain_NotFound` 断言返回 nil，这本身就是过时的；改成 `FindDomain_LazyCreatesZone`，断言返回有效 domain 且 store 里确实多了一条 zone。
- `qualifyName(name, apex)` 只在 `name` 不以 `.` 结尾时拼 `name+"."+apex`——即 `name` 必须是**相对短标签**（如 `"www"`），不能是完整域名（如 `"www.example.com"`，否则会拼成 `www.example.com.example.com.`）。原测试的 `CreateNSRecord`/`FindNSRecordWithNameAndType` 调用传的都是完整域名，恰好因为断言只看 `RouteTags`/数量、不看具体 `Name`，才没暴露这个用法错误；改用相对短标签（`"www"`/`"cdn"`），和 `internal/edgeagent/agent.go` 里同样的"相对标签"约定对齐。

**顺带修的一个真实生产 bug**：`edgeDNSListDomains`（`ListNSDomains` 分页）和 `listRecordsInZone`（`ListNSRecords` 分页）都是直接对 Go map（`ZoneStore.Snapshot()`/`zone.Records`）取值拼 slice 后做 offset/size 切片——map 遍历顺序不确定，意味着同一份数据不同时刻调用分页结果可能不一致（真实 GoEdge 分批拉取域名/记录列表时可能漏掉或重复）。改测试要求断言固定顺序时直接暴露了这一点（`TestEdgeDNS_ListDomains_Pagination` 需要稳定的 offset=1 结果）。已在 `edgedns_provider.go` 里给两处都加了排序（域名按 name，记录按 name+id）再分页，从测试驱动出的生产代码修复。

**踩坑**：`findDomainID` 测试 helper 最初直接用 `resp["data"].(map[string]any)["nsDomain"].(map[string]any)["id"].(float64)` 取 id 再塞回下一次请求，结果诡异地 404——id 是 `zoneID()`（FNV-1a 64位 hash）算出来的，常年超过 `float64` 精确整数范围（2^53），JSON 解码成 `float64` 再编码回请求体时精度丢失，导致回传的 id 和服务端重新计算的 hash 对不上。修复：解码响应时用 `json.NewDecoder(...).UseNumber()` + `json.Number.Int64()` 保精度，其余小数值（`code`/分页数量等）不受影响可以继续用默认 float64 解码。

验证：`go build ./...`、`go vet ./...`、`gofmt -l` 全干净；`go test ./...` 全绿（含之前失败的 `dns-edge/internal/api` 包）。

---

## 运行时配置陷阱

**Tea.Root 规则**：`Tea.Root = filepath.Dir(filepath.Dir(exePath))`（不是 cwd，是可执行文件路径决定的）

| 二进制路径 | Tea.Root | 配置目录 |
|-----------|----------|---------|
| `/home/ivloli/edgeapi-run/edge-api` | `/home/ivloli` | `/home/ivloli/configs/` |

所以 `edgeapi-run/configs/` 不会被读取，正确的配置在 `/home/ivloli/configs/`。

---

## 泛域名配置 SOP

### 通过 EdgeAdmin UI（推荐）

1. DNS → DNS 域名 → 新建（选 local-dns-edge）
2. 集群 → DNS → 绑 DNS 域名，DNS 名称填 `cluster1`
3. 网站 → 新建，域名 `*.hello.com`，配置后端

等约 20s，edgeapi 自动推送：A 记录（各节点 IP）、`* CNAME cluster1.hello.com`、网站 dnsName CNAME。

### 通过 DB 手动触发

```sql
-- 触发 clusterChange
INSERT INTO edgeDNSTasks (clusterId, serverId, nodeId, domainId, recordName, type, updatedAt, isDone, isOk, version)
SELECT id, 0, 0, dnsDomainId, '', 'clusterChange', UNIX_TIMESTAMP(), 0, 0, 1 
FROM edgeNodeClusters WHERE name='目标集群';
```

注意：节点必须 `isInstalled=1`，否则被 `FindAllEnabledNodesDNSWithClusterId` 过滤，A 记录不推送。

---

## NS 仪表盘"24小时无数据时坐标系整个消失"（2026-07-03）

用户反馈 `/ns` 仪表盘"近24小时"图表连坐标轴都没了。排查过程：

1. 先怀疑是 2026-07-02 修过的 `chart-box` 容器高度问题回归——确认 `ns/index.css` 还在、`<head>` 里 `<link>` 也还在，排除。
2. 用 curl 直接查 `POST /ns` 的原始 JSON，发现 `hourlyStats`/`topDomainStats`/`topNodeStats` 都是 `null`（不是 `[]`）——因为过去 24 小时窗口早已滑出了 2026-07-02 手工插入的种子数据范围（`edgeNSRecordHourlyStats` 最新一条是 `20260702 19:00`，早于当前时间 24 小时之前），DAO 查询返回 0 行是预期的。
3. 关键发现：**0 行结果不该导致整个前端崩溃**。`ns/index.js` 里 `reloadHourlyTrafficChart`→`reloadTrafficChart`→`teaweb.bytesAxis(stats, ...)` 对 `stats` 无条件调用 `.map()`；`stats` 一旦是 `null`（而不是空数组）就直接抛 `TypeError`，而这个异常发生在 `this.$delay(function(){ this.reloadHourlyTrafficChart(); this.reloadTopDomainsChart() })` 这个同步回调里，第一行抛出后第二行（域名排行图）根本不会执行——这就是"坐标系全没了"的真正原因，跟今天改的同步机制毫无关系，是**任何域名/集群只要在统计窗口内零流量就会必现**的通用 bug，不是这次种子数据过期才暴露的偶发情况。
4. **踩坑（protobuf 空 slice 语义）**：一开始只在 `edgeapi/internal/rpc/services/service_ns.go` 的 `ComposeNSBoard` 里把 `var hourlyTrafficStats []*pb.X` 改成 `var hourlyTrafficStats = []*pb.X{}`（非 nil 空切片），编译部署后用 curl 复测**仍然是 `null`**——因为 gRPC/protobuf 的 repeated 字段在 wire format 层面不区分"空切片"和"未设置"，序列化 0 个元素等于什么都不写，edgeadmin 客户端反序列化后拿到的字段永远是 Go 的 nil slice 零值，不管服务端赋的是 `nil` 还是 `[]T{}`。**这个字段级别的 nil/empty 差异过不了 gRPC 边界**，必须在真正生成 HTTP JSON 响应的那一层（也就是 edgeadmin 自己的 action，不是 edgeapi）显式初始化成非 nil 空切片才有效。

**修复**：
- `edgeapi/internal/rpc/services/service_ns.go`：`ComposeNSBoard` 里 4 个统计切片改成非 nil 初始化（`hourlyTrafficStats`/`dailyTrafficStats`/`topDomainStats`/`topNodeStats`）——这层其实过不了 gRPC 边界，属于防御性最佳实践，保留但不是关键修复。
- `edgeadmin/internal/web/actions/default/ns/index.go`：`RunPost` 里同名的 4 个本地变量也改成非 nil 初始化——**这里才是真正决定 HTTP JSON 输出的地方**，改完之后 curl 复测确认 `hourlyStats`/`topDomainStats`/`topNodeStats` 从 `null` 变成 `[]`（`len=0`），前端 `.map()` 不再崩溃。
- `cpuValues`/`memoryValues`/`loadValues` 同一个文件里也是 `var x [][]byte`（同样的 nil 风险），但 `ns/index.js` 根本没引用这三个字段（可能是抄别的看板模板留下的死字段），没有实际崩溃场景，没有跟着改。

**通用教训**：以后任何"列表类"gRPC 响应字段，只要客户端 JS 会无条件 `.map()`/`.forEach()`，就必须在最终生成 JSON 的那一层（通常是 edgeadmin 的 action，而不是 edgeapi 的 RPC service）显式保证非 nil——只在 edgeapi 侧初始化空切片是**无效的**，gRPC 序列化会把这个信息丢掉。
