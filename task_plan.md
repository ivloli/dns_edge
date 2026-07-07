# 任务计划

## 项目概述

**项目名称**: dns-edge — GoEdge 边缘 DNS 节点  
**定位**: GoEdge（edgeapi）的边缘 DNS 服务，双模工作：
1. **CDN 模式**：接收 edgeapi 通过 edgeDNSAPI（HTTP）推送的 CDN 记录，纯内存提供 DNS 解析
2. **NS 模式**：通过 edgeagent（gRPC）从 edgeapi 同步 NS 域名和记录，独立 DNS 权威服务

**Source of truth**: GoEdge（edgeapi），dns-edge 无持久化状态，仅缓存

## 架构

```
EdgeAdmin
    │
    ▼
edgeapi (gRPC :8031)
    ├── edgeDNSAPI (HTTP :8080) ──→ dns-edge ZoneStore → DNS :5300
    │   CDN 模式：edgeapi 主动 HTTP 推送记录
    │
    └── gRPC NS 服务 ──→ edgeagent (dns-edge 内)
        NS 模式：edgeagent 轮询任务，拉取 NSDomain/NSRecord

数据库: db_edge（统一，废弃 db_edge_comm）
配置目录: /home/ivloli/configs/（Tea.Root = /home/ivloli，即二进制父目录的父目录）
```

## 已完成专项

| 专项 | 内容 | 验证 |
|------|------|------|
| no-PG 模式 | edgeDNSAPI 直接读写 ZoneStore，零 PG 依赖 | ✅ |
| 自动恢复 | edgeapi 空检测 + ClusterChange 重推，≤20s 恢复 | ✅ |
| geo 修复 | parseRegion 字段对齐 + normalizeProvince/ISP | ✅ |
| xdb 自动更新 | GitHub Releases 定时拉取，热替换无重启 | ✅ |
| zoneCount | /healthz 暴露 zoneCount，O(1) 空检测 | ✅ |
| CNAME 恢复 | ClusterNodesChange → ClusterChange | ✅ |
| geo IP 聚合 | filterByGeo 按目标 IP 聚合，省+ISP 双标签命中 | ✅ |
| 动态权重 | edgeapi 按节点 load1m 计算 Weight，dns-edge 加权随机 | ✅ |
| 通配符 CNAME | edgeapi clusterChange 自动推 `* CNAME`，dns-edge RFC 4592 展开 | ✅ |
| NS 模块合并 | edgeapi-comm NS 代码合并到 edgeapi（feature/ivloli），统一 db_edge | ✅ |
| NS gRPC 鉴权修复 | CreateAPIToken 补加 + UserTypeDNS switch + ValidateNodeId | ✅ |
| edgeagent | dns-edge 内置 gRPC agent，轮询任务，同步 NSDomain/NSRecord | ✅ |
| applyRecord 记录名展开 | `@`→apex，`www`→`www.zone.`，convertRecordToPB 填充 NsDomain | ✅ |
| edgeagent 重连机制 | dialWithRetry 指数退避 + keepalive + 连接状态日志 + 心跳周期上报 | ✅ |
| edgeagent 冷启动全量重同步 | 连接建立后无条件 syncDomains+syncRecords，不等任务队列 | ✅ |

## 当前运行状态（2026-07-01）

| 组件 | 进程 | 配置 |
|------|------|------|
| edgeapi | `/home/ivloli/edgeapi-run/edge-api`，gRPC :8031 | `/home/ivloli/configs/`，db_edge |
| dns-edge | `./dns-edge-local -config Corefile.local`，DNS :5300，API :8080 | edgeagent → 127.0.0.1:8031 |
| MySQL | 127.0.0.1:3306 | db_edge（统一库，db_edge_comm 已废弃） |
| EdgeAdmin | :7788，admin/admin | — |

**联调验证结果**：

| 测试 | 结果 |
|------|------|
| `dig @127.0.0.1 -p 5300 test.local A` | `10.0.0.1` ✅（@ apex 记录） |
| `dig @127.0.0.1 -p 5300 www.test.local A` | `1.2.3.4` ✅（子域名记录） |
| nsRecordChanged 任务消费 | isDone=1 isOk=1 ✅ |
| nsDomainChanged 任务消费 | isDone=1 isOk=1 ✅ |

## 待做

| 优先级 | 任务 | 状态 |
|--------|------|------|
| P1 | EdgeAdmin NS 管理 UI — 接口层全部完成 | ✅ |
| P1-a | NS UI 前端 HTML + JS 视图（现只有骨架，接口已通） | ✅ |
| P1-b | NSService/NSPlanService/NSDomainGroupService 补齐（原缺失致 Unimplemented） | ✅ |
| P1-c | NS 全局设置（`/ns/settings`）— 复用 SysSettingService 实现，无需新 proto | ✅ |
| P1-d | 左侧「智能DNS」子菜单补全 + `/ns/clusters` 集群列表页缺失 | ✅ |
| P2 | ExtractNSClusterTask 定时器验证 | — |
| P3 | NS 模块端到端联调：EdgeAdmin UI → 任务 → dig 验证 | ✅ 2026-07-03 curl 模拟登录验证，见"测试报告"一节 |
| P5 | GoEdge customHTTP 联调 | — |
| P10 | edgeagent 重连机制 | ✅ |
| P11 | NS 记录线路（routeIds）页面无法设置，且线路匹配逻辑本身也未实现 | ✅ 2026-07-07 全部修完，见下方详情 |
| P12 | dns-edge 重启后个别域名不会自动恢复（zoneCount 非0时自动恢复不触发）| — 2026-07-06 发现，见下方详情 |

### P11 详情：NS 记录创建/编辑弹窗没有线路选择器（2026-07-06）

给现有 NS 记录批量加"中国-省份-ISP"线路时发现：**目前完全没法通过 EdgeAdmin 页面给记录设置线路**，只能直接改数据库（`edgeNSRecords.routeIds` 是个 JSON 数组，引用 `edgeNSRoutes.id`）。查了三处证实：

1. `edgeapi/internal/rpc/services/service_ns_record.go` 的 `CreateNSRecord`/`UpdateNSRecord` 两个 RPC，调用 DAO 时 `routeIds` 参数**硬编码传 `nil`**，不管请求里实际带了什么值都会被丢弃。
2. `edgeadmin/internal/web/actions/default/ns/records/createPopup.go`/`updatePopup.go` 后端 action 代码里完全没有 `routeIds`/线路相关字段。
3. 对应的 `createPopup.html`/`updatePopup.html` 前端模板里也没有线路选择器控件。

`edgeNSRoutes` 表本身（存线路定义，`code` 前缀 `country:`/`province:`/`isp:` 是内置线路，其余是自定义线路）目前是空的——说明这批"内置线路"数据从来没有被种过，第一次要用还得先手工插入（已经在这台开发机上插了 7 条：`country:中国`/`province:上海`/`广东`/`北京`/`isp:电信`/`移动`/`联通`，`id` 1-7）。

**要修的话**：`CreateNSRecord`/`UpdateNSRecord` 把 `nil` 改成 `req.RouteIds`（proto 里应该已经有这个字段，只是没接上）；再给两个弹窗补上线路多选框（复用 `/ns/routes` 页面已有的 `FindAllDefaultChinaProvinceRoutes`/`FindAllDefaultISPRoutes`/`FindAllNSRoutes` 接口拉列表）。

**2026-07-07 补充调研，范围比最初以为的大得多**：这不是"接一下参数就行"的小修。查了 `dns_dev/internal/edgeagent/agent.go`（NS 模式的拉取端）全文，**压根没有任何处理 `routeIds`/线路匹配的代码**——`applyRecord` 直接把记录塞进 `iface.Record{}`，不看路线字段。也就是说即使把 edgeapi 的 RPC 接上、edgeadmin 页面加上线路选择框，让用户能给 NS 记录选线路，**dns-edge 实际解析请求时也不会用这个字段做任何区分**，保存的线路选择纯粹是摆设。

对比之下，"按线路/ECS 子网返回不同记录"这个能力**目前只存在于 CDN 模式**（`dns_dev/internal/api/edgedns_provider.go`，走 ip2region，已经用 `+subnet=` 验证过、写进了 `docs/cdn-cluster-geo-dig-test.md`）。用户回忆的"边缘节点记录能配线路、dig 能测 ECS 匹配"说的就是这套，跟 NS 模式是完全独立的两套代码路径。

要让 NS 模式的线路真正生效，除了上面 P11 原本列的 RPC/UI 工作，还需要**在 dns-edge 里新写一段 NS 模式的线路匹配逻辑**（读 ECS 子网 → 查 ip2region 或类似方式 → 按 `routeIds` 过滤候选记录），工作量接近于把 CDN 模式已有的 geo 路由能力在 NS 模式这条代码路径里重新实现一遍，不是一个小任务，需要单独排期。

**2026-07-07 完成**：调研后发现范围比想象中小——`filterByGeo`（`internal/dns/handler.go`）本来就是通用的，`iface.Record.RouteTags`/`ZoneStore` 多记录支持也早就有，查询路径完全不用改，`pb.NSRecord.NsRoutes` 协议字段也早就定义好了，只是没人填。实际改动：
- edgeapi `service_ns_record.go`：`CreateNSRecord`/`UpdateNSRecord` 接上 `req.NsRouteIds`；`convertRecordToPB` 反查 `routeIds` 填充 `NsRoutes`。
- edgeadmin：`ns/records/createPopup.go`/`updatePopup.go` + 对应模板加线路多选（复用 `FindAllDefaultChinaProvinceRoutes`/`FindAllDefaultISPRoutes`）。
- dns-edge：新包 `internal/nsroute`（`CodesToTags`，从 CDN 模式的 `nsRouteCodesToTags` 抽出来给两边共用），`edgeagent/agent.go` 的 `applyRecord` 用它填充 `RouteTags`。
- 只支持内置线路（`country:`/`province:`/`isp:` 前缀），不支持自定义 IP 段/CIDR/地域 ID 线路，跟 CDN 模式的实际能力对齐（CDN 那边同样只处理 `code`，不解析 `RangesJSON`）。
- 本机端到端验证通过（同名多线路记录 + `+subnet=` 精确匹配/无 ECS 随机/境外 IP 兜底三种场景），详见 `docs/ecs-geo-routing-design.md` 第 9 节。
- 全程只在本机验证，**没有部署到今天已经上线的贵州/新加坡测试环境**，也没有推送到远程仓库，等你确认后再说。
- 顺带发现并修复一个独立 bug：NS 记录/域名软删除后，增量同步（10s 轮询）没有及时把删除同步给 dns-edge，要等 dns-edge 重启触发全量同步才清掉。根因：`NSRecordDAO.DeleteNSRecord`/`NSDomainDAO.DeleteNSDomain` 只把 `state` 置为禁用，**没有更新 `version` 列**，而 `ListNSRecordsAfterVersion`/`ListNSDomainsAfterVersion` 增量同步是靠 `WHERE version > ?` 过滤的——删除时 version 不变，这条记录就永远不会再出现在增量结果里，等于对增量同步"隐身"了。两个 DAO 的 `Delete*` 方法都补上了 `Set("version", time.Now().UnixNano())`（跟 `Create`/`Update` 用的是同一套 version 生成方式）。本机验证：不重启 dns-edge，纯靠 10 秒轮询，删除后正确落回通配符兜底。

### P12 详情：CDN 模式记录"部分丢失"不会被自动恢复机制发现（2026-07-06）

背景：本机重启 dns-edge 很多次后，用户发现 `fafa.com`/`momo.com` 两个 CDN 域名 dig 不出来（REFUSED），但同一时间 `test.local`/`example.com`/`mysite.io` 是正常的。排查确认 **GoEdge 侧数据完好无损**（`edgeDNSDomains` 表里 fafa.com/momo.com 的记录 JSON 一条没少），只是 dns-edge 内存里这两个 zone 没了。

**根因**：edgeapi 的"自动恢复"（`dns_task_executor.go` 的 `resyncEmptyEdgeDNSProviders()`）只在 dns-edge **整体 zoneCount 变成 0** 时才会触发重推——判断的是"这个 DNS 服务商名下是不是完全没域名了"，不是"逐个域名检查是否还在"。因为 test.local 等域名一直在（zoneCount 从没真正归零过），fafa.com/momo.com 这两个虽然掉了，却一直不会被自动拉回来。

**临时恢复方法**：EdgeAdmin → DNS 管理 → 对应 DNS 服务商 → 找到域名 → 点"同步"（本质是把 `edgeDNSTasks` 里对应集群的 `clusterChange` 任务重置成待处理，等 `DNSTaskExecutor` 下一轮 20s tick 处理）。

**要修的话**：`resyncEmptyEdgeDNSProviders` 或类似巡检逻辑应该改成逐个域名核对 dns-edge 实际有的 zone 列表和 GoEdge 侧启用的 DNS 域名列表，有差异就单独补推那个域名，而不是只看整体 zoneCount 是否为 0。

## EdgeAdmin NS UI — 接口实现状态（2026-07-01）

接口全部编译通过（`go build ./internal/web/... EXIT:0`）。

### 已实现路由（共 26 个）

| 模块 | 路由 | RPC |
|------|------|-----|
| 仪表盘 | `GET+POST /ns` | `NSService.ComposeNSBoard` |
| 集群 | `GET /ns/clusters/cluster` | `NSClusterRPC.FindNSCluster` |
| 集群 | `GET+POST /ns/clusters/createPopup` | `NSClusterRPC.CreateNSCluster` |
| 集群 | `GET+POST /ns/clusters/updatePopup` | `NSClusterRPC.UpdateNSCluster` |
| 集群 | `POST /ns/clusters/delete` | `NSClusterRPC.DeleteNSCluster` |
| 节点 | `GET+POST /ns/nodes/createPopup` | `NSNodeRPC.CreateNSNode` |
| 节点 | `GET+POST /ns/nodes/updatePopup` | `NSNodeRPC.UpdateNSNode` |
| 节点 | `POST /ns/nodes/delete` | `NSNodeRPC.DeleteNSNode` |
| 域名 | `GET /ns/domains` | `NSDomainRPC.Count+List` |
| 域名 | `GET+POST /ns/domains/createPopup` | `NSDomainRPC.CreateNSDomain` |
| 域名 | `GET+POST /ns/domains/updatePopup` | `NSDomainRPC.UpdateNSDomain` |
| 域名 | `POST /ns/domains/delete` | `NSDomainRPC.DeleteNSDomain` |
| 域名分组 | `POST /ns/domains/groups/options` | `NSDomainGroupRPC.FindAllAvailable` |
| 域名分组 | `GET+POST /ns/domains/groups/createPopup` | `NSDomainGroupRPC.CreateNSDomainGroup` |
| 域名分组 | `POST /ns/domains/groups/delete` | `NSDomainGroupRPC.DeleteNSDomainGroup` |
| 记录 | `GET /ns/records` | `NSRecordRPC.Count+List` |
| 记录 | `GET+POST /ns/records/createPopup` | `NSRecordRPC.CreateNSRecord` |
| 记录 | `GET+POST /ns/records/updatePopup` | `NSRecordRPC.UpdateNSRecord` |
| 记录 | `POST /ns/records/delete` | `NSRecordRPC.DeleteNSRecord` |
| 线路 | `GET+POST /ns/routes` | `NSRouteRPC.FindAllNSRoutes` |
| 线路 | `GET+POST /ns/routes/createPopup` | `NSRouteRPC.CreateNSRoute` |
| 线路 | `GET+POST /ns/routes/updatePopup` | `NSRouteRPC.UpdateNSRoute` |
| 线路 | `POST /ns/routes/delete` | `NSRouteRPC.DeleteNSRoute` |
| 线路分类 | `GET+POST /ns/routes/categories` | `NSRouteCategoryRPC.FindAll` |
| 线路分类 | `GET+POST /ns/routes/categories/createPopup` | `NSRouteCategoryRPC.Create` |
| 线路分类 | `GET+POST /ns/routes/categories/updatePopup` | `NSRouteCategoryRPC.Update` |
| 线路分类 | `POST /ns/routes/categories/delete` | `NSRouteCategoryRPC.Delete` |
| 套餐 | `GET+POST /ns/plans` | `NSPlanRPC.FindAllNSPlans` |
| 套餐 | `GET+POST /ns/plans/createPopup` | `NSPlanRPC.CreateNSPlan` |
| 套餐 | `GET+POST /ns/plans/updatePopup` | `NSPlanRPC.UpdateNSPlan` |
| 套餐 | `POST /ns/plans/delete` | `NSPlanRPC.DeleteNSPlan` |
| 设置 | `GET /ns/settings` | 暂无 RPC（pb 中无 NS 全局设置） |

### rpc_client.go 新增
- `NSRPC()` → `NSServiceClient`
- `NSRouteRPC()` → `NSRouteServiceClient`
- `NSRouteCategoryRPC()` → `NSRouteCategoryServiceClient`
- `NSPlanRPC()` → `NSPlanServiceClient`
- `NSDomainGroupRPC()` → `NSDomainGroupServiceClient`

### 参考文档
- `docs/ns-crawl-report.html` — 商业版接口爬取报告
- `docs/ns-api-summary.html` — 我们实现的接口汇总

## edgeapi 新增 NS 服务（2026-07-01）

edgeadmin 重启后 `/ns` 报 `rpc error: Unimplemented desc = unknown service pb.NSService`，
说明 pb 里定义了 `NSService`/`NSPlanService`/`NSDomainGroupService`，但 edgeapi 从未实现和注册。

**新增 DAO**（`edgeapi/internal/db/models/nameservers/`）：
- `ns_plan_dao.go` — CreateNSPlan / UpdateNSPlan / DeleteNSPlan / FindEnabledNSPlan / FindAllNSPlans
- `ns_domain_group_dao.go` — CreateNSDomainGroup / UpdateNSDomainGroup / DeleteNSDomainGroup / FindAllNSDomainGroups / FindAllAvailableNSDomainGroups / FindEnabledNSDomainGroup

**新增 Service**（`edgeapi/internal/rpc/services/`）：
- `service_ns.go` — `NSService.ComposeNSBoard`（当前只填计数，图表统计留空，无 hourly stat DAO）、`ComposeNSUserBoard`
- `service_ns_plan.go` — `NSPlanService` 全部 RPC
- `service_ns_domain_group.go` — `NSDomainGroupService` 全部 RPC

**注册**：`edgeapi/internal/nodes/api_node_services.go` 新增三个 `pb.RegisterXXXServiceServer` 调用块（紧跟 `NSNodeService` 之后）。

**关键点**：`CountAllEnabledClusters`/`CountAllNSNodes` 在 `internal/db/models` 包（不是 `nameservers` 子包），`CountNSRecords` 才在 `nameservers` 包但要求 domainId 参数（传 0 表示全局）。混用两个包时注意导入路径。

## 左侧菜单缺子项 + 集群列表页缺失（2026-07-02）

浏览器验证时发现：`/ns` 仪表盘能显示，但左侧「智能DNS」菜单点开后没有二级子菜单，且仪表盘上「NS集群」卡片点进去是 404/无效参数。

**根因**：
1. `internal/web/helpers/menu.go` 里 `FindAllMenuMaps` 的 `ns` 组 `subItems` 只写了 2 项（集群管理指向 `/ns`、域名管理），漏了线路管理/套餐设置/全局配置。TeaGo layout（`@layout.html`）直接遍历 `module.subItems` 渲染二级菜单，条目不全就是空的。
2. `/ns/clusters`（集群列表页）从未实现 Action——`init.go` 只注册了 `/ns/clusters/cluster`（详情，需 `clusterId`）、`createPopup`、`updatePopup`、`delete`，没有列表入口。仪表盘卡片链接指向 `/ns/clusters/cluster`（不带参数），点开必然出错。

**修复**：
- `menu.go`：`ns` 组 `subItems` 补全为 仪表盘(`/ns`)、集群管理(`/ns/clusters`)、域名管理(`/ns/domains`)、线路管理(`/ns/routes`)、套餐设置(`/ns/plans`)、分隔线、全局配置(`/ns/settings`)，文案直接用 edgecommon `codes.go` 里已有的 `AdminMenu_NS*`（对齐商业版翻译）。
- 新建 `internal/web/actions/default/ns/clusters/index.go`：`FindAllNSClusters` + 逐个 `CountAllNSNodesMatch` 拼节点数，渲染集群列表。
- 新建 `web/views/@default/ns/clusters/index.html`+`index.js`：列表 + 增删改弹窗（复用已有的 createPopup/updatePopup/delete 路由）。
- `init.go`：`/ns/clusters` 加 `Get("", new(clusters.IndexAction))`；根路由 `/ns` 的 `teaSubMenu` 从误设的 `"cluster"` 改成 `"dashboard"`（跟菜单新增的仪表盘项对应）。
- `ns/index.html` 仪表盘卡片链接从 `/ns/clusters/cluster` 改成 `/ns/clusters`。

**验证**：`CGO_ENABLED=0 go build ./...` 通过（edgeadmin 必须 `CGO_ENABLED=0`，否则会因 `internal/waf/injectionutils` 下未加 build tag 的 `.c` 文件报错 "C source files not allowed when not using cgo"）。已重新编译部署，`curl -A "Mozilla/5.0" /` 返回 200。

**浏览器截图复查后发现同一问题的真正根因**：以上修复后菜单仍未展开。用浏览器截图确认后定位到 `web/views/@default/@layout.html` 里子菜单的展开条件是 `v-if="teaMenu == module.code"`（不是 `teaSubMenu`），而 `teaMenu` 由路由 `Data("teaMenu", "ns")` 设置——**`/ns/init.go` 整组路由从未设置过 `teaMenu`**，只设了 `teaSubMenu`，所以左侧「智能DNS」菜单永远无法判定为当前激活模块，子菜单条件式恒为 false。

TeaGo 的 `Server.Data()/EndData()` 不是栈式作用域：`lastData` 是单一共享字段，`EndData()` 直接把它置为 `nil` 而非弹栈恢复外层值（见 `TeaGo/server.go:655-667`）。因此只在最外层 `Prefix("/ns").Data("teaMenu","ns")` 设置一次是不够的——`/ns/clusters`、`/ns/domains` 等每个内层 `Prefix().Data()...EndData()` 块都会先清空再设置自己的 `teaSubMenu`，拿不到外层的 `teaMenu`。**修复：在 `ns/init.go` 里每一个内层子分组的 `Data()` 链上都补一份 `Data("teaMenu", "ns")`**（clusters/nodes/domains/records/routes/plans/settings 共 7 处），而不是只设一次。参照的正确写法可见 `clusters/init.go`（无内层分组，一次 `Data("teaMenu","clusters")` 管到 `EndAll()`）——`ns/init.go` 的路由结构因为有多个子模块分组，不能照抄这种单次设置的模式。

排查这次问题时也确认了一个之前的误判：**Tea.Root 与进程 cwd 无关**，是 `bootstrap/init.go` 的 `init()` 用 `filepath.Dir(filepath.Dir(二进制真实路径))` 算出来的（TeaGo 内部机制，与 `Tea` 包自身 `findRoot()` 的 `os.Getwd()` fallback 不同，后者会被 bootstrap 包的 `init()` 覆盖，只要程序 import 了 `TeaGo/bootstrap`）。所以从任意目录启动二进制，只要二进制路径不变，Tea.Root/views/configs 解析结果都一样，之前怀疑"部署路径不对"是排查方向错误，浪费了不少时间——下次遇到 TeaGo 相关路径问题应直接读 `bootstrap/init.go`，不要只看 `Tea/tea.go`。

## NS 全局设置功能实现（2026-07-02）

`/ns/settings` 之前是纯占位页（"NS 全局设置功能暂未开放"）。原计划是仿照其他 NS 模块新增专用 `service_ns_setting.proto`，但排查后发现项目里已有通用配置服务 `SysSettingService`（`edgecommon/pkg/rpc/protos/service_sys_setting.proto`：`code string` + `valueJSON bytes`），edgeadmin 的管理员安全设置（`adminSecurityConfig`）、访问日志队列设置（`accessLogQueue`）等都是走这个机制存取 JSON blob，edgeapi 侧早已实现并注册（`service_sys_setting.go`）。**改为直接复用这套机制，完全不需要新增 proto、不需要 protoc、不需要碰 edgeapi**——这是比"仿照其他模块建专用 proto"更一致、成本更低的方案。

**字段设计依据**：`docs/ns-feature-requirements.html` 第 7 节（2026-06-30 已对商业版调研过的字段级细节）。首次实现时还没有商业版登录凭证，先基于这份既有文档推进；同日拿到凭证（`adminGain` / `o68dCMKcuWfT2QOr`）后已补一次实测核对，发现并修正了 3 处字段偏差，详见下方"商业版实测核对"小节。

**新增**：
- `edgecommon/pkg/nsconfigs/ns_settings_config.go`：`NSPlanConfig`（国家/省份/运营商/Agent/公共线路开关、最大自定义线路数、最小TTL、最大域名数、每域名最大记录数，另见下方实测补充字段）+ `NSSettingsConfig`（默认集群ID、`DefaultPlanConfig`）+ `NSAccessLogSettingsConfig`（独立结构体，见下方实测核对）。edgeapi 侧不需要引用此包（`SysSettingService` 只存取不透明字节），edgeapi 的 go.mod 也没有走本地 edgecommon replace（用的是 versioned v1.3.9），这次改动因此**零风险**：edgeapi 完全不用重新编译部署。
- `edgecommon/pkg/systemconfigs/settings.go`：新增 `SettingCodeNSSettings = "nsSettings"`。
- `edgeadmin/internal/web/actions/default/ns/settings/index.go`：重写，GET 用 `SysSettingRPC().ReadSysSetting` 读取（为空则用 `NewNSSettingsConfig()` 默认值），同时 `NSClusterRPC().FindAllNSClusters` 拉集群列表供下拉框；POST 校验（`minTTL`/`maxDomains`/`maxRecordsPerDomain`/`maxCustomRoutes` 需 ≥0）后 `SysSettingRPC().UpdateSysSetting` 保存。
- `edgeadmin/web/views/@default/ns/settings/index.html`+`index.js`：整页表单（非弹窗，参照 `servers/logs/settings.html` 的模式），替换掉原来的"暂未开放"提示。
- `ns/init.go`：`/ns/settings` 路由从 `Get` 改成 `GetPost`（之前漏掉导致 POST 404）。

**踩坑**：`<textarea>` 不能用 `{{}}` 插值双向绑定（Vue 2 对 textarea 插值支持不可靠），必须用 `v-model` 绑定字符串字段。这个坑是在初版"手填 clusterHosts"实现里踩到的，后来发现 clusterHosts 语义理解错误（见下方实测核对）已改为只读展示，不再用 textarea，但这条 Vue/TeaGo 通用经验仍然有效，其他页面用 textarea 时要记得。

**已知缺口**：`/ns/settings` 只读展示"默认集群的主机名"，但 `ns/clusters/createPopup.go`/`updatePopup.go` 目前都不支持编辑 `hosts` 字段（只能建集群时传空），所以这个只读展示目前永远是空的，除非直接改数据库。真正能编辑集群 hosts 要等"集群设置完整子菜单"（task_plan 里 P3 前置、`ns-feature-requirements.html` 4.3 节，10个子tab）做出来。已用直接 UPDATE 数据库的方式验证过展示代码本身没问题（能正确读出 `["ns1.mytest.com","ns2.mytest.com"]`）。

**验证**：`CGO_ENABLED=0 go build ./...` 通过。用之前发现的 `/csrf/token` 端点（返回值可直接用于登录/表单提交，绕开了此前认为"CSRF 无法用 curl 模拟"的限制）跑了完整 curl 冒烟测试：登录 → GET 表单渲染正常 → POST 保存返回 200 → 再 GET 确认 `window.TEA.ACTION.data.config` 与提交值完全一致，持久化验证通过。全部新字段（`maxLoadBalanceRecordsPerRecord`/`supportRecordStats`/`supportDomainAlias`/`supportHealthCheck`/`supportAPI`/`domainValidation.isOn`/`accessLogConfig.{isOn,logMissingDomains,missingRecordsOnly}`）逐一提交并回读确认一致。

### 商业版实测核对（同日，拿到凭证后补测）

拿到 `adminGain`/`o68dCMKcuWfT2QOr` 后用同一套 curl 自动化登录（`GET /` 拿 login token → `GET /csrf/token` 拿 CSRF → `POST /` 登录）登录了 172.31.32.12:7788，直接抓取 `/ns/settings/user`、`/ns/settings/accesslogs` 的 `window.TEA.ACTION.data` 真实 JSON，发现和初版实现有 3 处偏差，已修正：

1. **`defaultPlanConfig` 漏了 5 个字段**：`maxLoadBalanceRecordsPerRecord`（默认100，每记录最大负载均衡记录数）、`supportRecordStats`（默认true）、`supportDomainAlias`（默认false）、`supportHealthCheck`（默认false）、`supportAPI`（默认false）。已补全。
2. **整个 `domainValidation` 板块完全缺失**：`{isOn: bool, resolvers: []string}`，域名归属验证开关+自定义DNS解析器。新增 `NSDomainValidationConfig` 结构体，作为 `NSSettingsConfig` 的子字段。
3. **`clusterHosts` 语义理解错误**：原以为是"新建集群默认主机名"，做成了手填 textarea 持久化字段。爬取 `/_/@default/ns/settings/user/index.js` 才发现真相——它是**只读派生数据**：选中"默认集群"后异步 `POST .clusterHosts` 展示该集群自身已配置的 hosts，不是独立可编辑的全局设置项。已改为：`ClusterHosts` 从 `NSSettingsConfig` 中移除，GET 时若 `defaultClusterId>0` 则实时 `NSClusterRPC().FindNSCluster` 查询该集群的 `Hosts` 字段，只读展示；不做切换下拉时的实时 AJAX 刷新（这是简化点，保存后刷新页面即可看到新集群的 hosts，可接受）。
4. **访问日志设置字段对齐**：商业版是 `{isPrior, isOn, logMissingDomains, missingRecordsOnly}`。`isPrior`（是否覆盖）在集群级别才有意义，全局层跳过；其余 3 个字段（`isOn`/`logMissingDomains`/`missingRecordsOnly`）补全，改用独立的 `SettingCodeNSAccessLogSettings` 存取（原来错误地把 `accessLogIsOn` 塞进了 `NSSettingsConfig`，现在拆成两个独立 SettingCode，贴近商业版"用户设置"和"访问日志设置"两个页面对应两份数据的模型）。

**踩坑（追加）**：curl 无法模拟登录的判断是错的——`<csrf-token>` 组件的 token 并非服务端直接渲染进页面，而是通过 `GET /csrf/token` 异步获取（`internal/web/actions/default/csrf/token.go`，`csrf.Generate()` 生成后存入进程内 `sharedTokenManager`，单次消费+30分钟有效期）。只要先 `GET /csrf/token` 拿到有效 token 再提交表单，curl 完全可以完整走通登录+POST 流程，不需要真实浏览器。这个发现同样适用于以后爬测商业版或自己的 edgeadmin。

## NS 仪表盘图表数据补全（2026-07-02）

`/ns` 仪表盘的计数卡片能显示，但流量趋势图、域名访问排行图表全是空的——因为 `ComposeNSBoard`（首次实现时）只填了 count 字段，`HourlyTrafficStats`/`DailyTrafficStats`/`TopNSDomainStats`/`TopNSNodeStats` 从未真正查询过，且 `edgeNSRecordHourlyStats`（按小时聚合的流量表）本身是空表——开发环境没有真实 DNS 查询流量会写入这张表。

**新增**：
- `edgeapi/internal/db/models/nameservers/ns_record_hourly_stat_dao.go`：新建 DAO（之前只有 model 没有 DAO），4个聚合查询方法——`FindHourlyStatsBetweenHours`（按小时GROUP BY，供24小时趋势图）、`FindDailyStatsBetweenDays`（按天GROUP BY，供15天趋势图）、`FindTopDomainStatsBetweenHours`（按domainId GROUP BY + 按请求数降序 + LIMIT，供域名排行）、`FindTopNodeStatsBetweenHours`（按nodeId GROUP BY，供节点排行）。
- `edgeapi/internal/rpc/services/service_ns.go`：`ComposeNSBoard` 补全图表数据的查询+组装逻辑，域名/节点名称通过 `FindEnabledNSDomain`/`FindEnabledNSNodeName` 二次查询补上（DAO 聚合查询只返回ID，不含名称）。
- `edgeapi/internal/db/models/nameservers/ns_record_dao.go`：新增 `CountAllNSRecords(tx)`，修了一个从第一版就带着的 bug——`CountNSRecords(tx, 0, "", "", "")` 里 `domainId=0` 不是"不限制"的意思，DAO 内部无条件 `Attr("domainId", domainId)`，传0实际是查询"domainId恰好等于0的记录"（永远查不到真实记录），导致仪表盘 `countRecords` 一直显示0。

**踩坑**：`FindTopNodeStatsBetweenHours` 的 SQL 同时 `SELECT nodeId, clusterId` 但只 `GROUP BY nodeId`，触发 MySQL `sql_mode=only_full_group_by` 报错（`clusterId` 未出现在 GROUP BY 里，MySQL 不知道它和 nodeId 是否函数依赖）。修复：`GROUP BY` 也加上 `clusterId`（业务上一个节点只属于一个集群，加了不影响分组基数）。

**种子数据**：为了让仪表盘图表看得见东西，手工插入了测试数据（仅本地开发环境）：
- 新增 2 个域名（`example.com`、`mysite.io`，均挂在 cluster 1 下），顺带修好了 `edgeNSRecords` 里两条指向不存在 domainId=4 的脏数据（历史遗留，插入 id=4 的域名后变成合法引用）。
- 用 Python 脚本生成 114 行 `edgeNSRecordHourlyStats` 种子数据：近24小时每小时一条（工作时段 8-22点权重更高，模拟日间高峰）+ 近14天每天一条，覆盖 3 个域名/集群1/节点1。纯本地测试用，不代表真实流量。

**验证**：`go build ./...`（edgeapi）通过，用 curl 登录后 `POST /ns` 拿到完整 JSON——`countRecords:7`（之前是0）、15条daily点、23条hourly点、3个域名排行（example.com/test.local/mysite.io）、1个节点排行，全部有真实数字。

### 图表"连坐标系都没有"追加排查（同日）

数据修好后用户反馈图表区域还是完全空白，连ECharts的坐标轴占位都没画出来——这不是数据问题，是纯前端问题。

**根因**：对比 `/dashboard`（GoEdge主仪表盘）发现它有专属的 `dashboard/index.css`，定义了 `.chart-box { height: 14em; }`；TeaGo 按目录自动加载同名 CSS（跟自动加载 `index.js` 同一套机制——文件存在才会在页面 `<head>` 里插入 `<link>` 标签，不存在就完全不插）。`ns/index.html` 从建立以来就只有 `index.html`+`index.js`，从没建过 `index.css`。ECharts 的容器 div 如果没有显式高度，`chart.resize()` 算出来是0高度，整个图表（含坐标轴）都不会渲染——这跟数据有没有完全无关，纯粹是容器尺寸为0。

**修复**：新建 `web/views/@default/ns/index.css`，内容照抄 `dashboard/index.css` 里跟我们页面相关的两条规则（`.chart-box` 高度 + `h4 span` 小字灰色，后者对应我们"域名访问排行（24小时）"标题里的副标题样式）。CSS是纯静态文件，不需要重编译二进制，重启edge-admin后用curl验证页面`<head>`里确实出现了`<link href=".../ns/index.css">`标签且内容正确。

**通用教训**：以后新建 NS 下任何页面，如果用到 `chart-box`/`columns-grid`/`chart-columns-grid` 这些依赖显式尺寸的布局组件，必须同步检查是否需要建一个对应的 `index.css`（参考同类已有页面，比如 `dashboard/index.css`），不能想当然认为全局 `@layout.css` 会兜底——它只管 grid 的 margin/border，不管子元素的实际高度。

## 新增文档：智能DNS模块说明（2026-07-02）

`dns_dev/docs/ns-modules-guide.html`：给用户看的模块说明文档，用大白话解释左侧「智能DNS」菜单每一项是干什么的（数据看板/集群管理/域名管理/线路管理/套餐设置/全局配置），包括一段整体架构图解（EdgeAdmin → edgeapi → dns-edge → 终端用户 dig 的数据流）、关键概念对照表（集群 vs 节点、域名 vs 记录、线路是什么、套餐限制什么）、以及当前版本已知的功能缺口标注（集群设置完整子菜单待开发、套餐能力字段待做等）。风格延续 `ns-api-summary.html`/`ns-crawl-report.html` 的视觉体系，保证这套文档看起来是一个系列。

## 多实例端口/凭证排查记录（2026-07-01）

调试 edgeadmin 连不上 edgeapi 时发现本机同时存在多份 edgeapi/edgeadmin 部署，配置散落、互相打架。记录现状供后续排查：

- 数据库 `edgeAPINodes` 表里有 2 条 `state=1` 记录：id=1（uniqueId `db8cdbdc...`, 监听 **8031**），id=2（uniqueId `e4de7b5b...`, 监听 **8033**）。用哪个 nodeId 启动 edge-api，实际监听端口以数据库里对应记录的 `http.listen.portRange` 为准，与 `secret` 无关。
- `edgeadmin` 侧的 `api_admin.yaml`（`rpc.endpoints`+`nodeId`+`secret`）必须和 edgeapi 实际运行时使用的那个 nodeId **完全对应**，否则鉴权失败，表现为 admin 页面所有请求 403（不是接口报错，是 HTTP 层直接拒绝，日志里无记录）。
- edgeadmin 有多份 `configs/api_admin.yaml`（仓库根 `configs/`、`build/configs/`），**运行时只认二进制同级或上级目录的那份**，改配置务必确认改的是被实际加载的文件。
- **curl 测试会被反爬虫规则拦截**：`internal/web/helpers/utils.go` 的 `spiderRegexp` 把 `curl`/`wget`/`python`等 UA 视为爬虫直接 403。测试要带 `-A "Mozilla/5.0"`。
- **CSRF 无法用 curl 模拟登录**：token 由 `internal/csrf/utils.go` 的 `sharedTokenManager`（进程内存）生成并单次消费，不是纯算法可推导，必须走真实浏览器完成登录流程。
- **已统一**：`edge-api`（`/home/ivloli/edgeapi-run/edge-api`）与 `edge-admin`（`/home/ivloli/Git_repo/edgeadmin/build/edge-admin`）都用 nodeId `db8cdbdc0136fe9f05a63f912a92c424`，端口 8031。两侧配置文件：`/home/ivloli/edgeapi-run/configs/api.yaml` 与 `/home/ivloli/Git_repo/edgeadmin/build/configs/api_admin.yaml`。curl 探测 `/` 返回 200，RPC 连接已恢复，待浏览器登录验证 NS 页面。

## 关键决策记录

| 日期 | 决策 | 理由 |
|------|------|------|
| 2026-06-25 | GoEdge 调用 dns-edge，而非反向 | dns-edge 是 GoEdge 的 DNS Provider |
| 2026-06-26 | edgeDNSAPI 全部走 ZoneStore，不依赖 PG | GoEdge 是 source of truth，dns-edge 纯内存缓存 |
| 2026-06-26 | FindNSDomainWithName lazy-create zone | GoEdge 不调用 CreateNSDomain |
| 2026-06-26 | 自动恢复在 edgeapi 侧实现 | dns-edge 无法反向调 edgeapi |
| 2026-06-27 | zoneCount O(1) 检测 | GetDomains 在百万域名场景每 20s 发大量请求 |
| 2026-06-27 | ClusterChange 替换 ClusterNodesChange | nodesOnly=true 跳过 CNAME 推送 |
| 2026-06-29 | 动态权重在 edgeapi 侧计算 | dns-edge 无状态，无法感知节点负载 |
| 2026-06-29 | 通配符 CNAME 在 edgeapi clusterChange 里自动推 | 本地 DNS 场景无外部 DNS 服务商 |
| 2026-06-30 | NS 节点用 UserTypeDNS 而非 UserTypeNode | DNS 节点 token type="dns"，UserTypeNode 会被拒绝 |
| 2026-06-30 | edgeapi-comm 代码合并到 edgeapi，废弃双库 | 统一一套系统，消除 sock 冲突和运维复杂度 |
| 2026-07-01 | convertRecordToPB 填充 NsDomain.Name | agent applyRecord 需要 zone 名展开相对记录名 |
| 2026-07-01 | Tea.Root = 二进制父目录的父目录 | edgeapi-run/edge-api → Tea.Root=/home/ivloli，配置在 /home/ivloli/configs/ |
| 2026-07-03 | edgeagent 连接建立后无条件全量 sync，不等任务队列 | dns-edge 无持久化状态，重启后若无 pending task 则 NS 数据永久丢失；agent-initiated 全量拉取是"pull 架构"下等价于 CDN 模式 zoneCount 自动恢复的方案，无需改 edgeapi |
| 2026-07-03 | edgeDNSAPI 分页统一排序（域名按 name，记录按 name+id） | Snapshot()/zone.Records 是 map，遍历顺序不确定，不排序会导致分页结果不稳定 |
| 2026-07-03 | edgedns_test.go fixture 改用真实 store.RWMutexStore，不再用 mockRS | edgedns_provider.go 全部处理函数只读写 ZoneStore（no-PG 迁移的结果），继续在 mockRS 上造数据只是自欺欺人 |

## 测试报告（2026-07-03）

本轮改动：P10 edgeagent 重连机制实现 + 冷启动全量重同步 + edgedns_test.go 12 个失效测试修复（含 2 个生产 bug）+ EdgeAdmin NS 集群详情页 `countNodes` 缺失修复。

### 1. 单元测试

| 项目 | 结果 |
|------|------|
| `go build ./...`（dns-edge） | ✅ 通过 |
| `go vet ./...`（dns-edge） | ✅ 干净 |
| `gofmt -l`（dns-edge） | ✅ 无输出 |
| `go test ./...`（dns-edge） | ✅ 全绿，含修复前失败的 `dns-edge/internal/api` 包（12 个测试） |
| `CGO_ENABLED=0 go build ./...`（edgeadmin） | ✅ 通过 |

### 2. 编译 + 本地重启

| 服务 | 二进制 | 结果 |
|------|--------|------|
| dns-edge | `dns-edge-local`（本次改动重新编译） | ✅ 重启后 DNS:5300 / API:8080 正常监听 |
| edge-api | 未改代码，仅重启用于测试重连 | ✅ 恢复正常 |
| edge-admin | `build/edge-admin`（countNodes 修复后重新编译） | ✅ 重启后 7788 正常监听 |

### 3. NS 功能端到端验证

| 测试项 | 方法 | 结果 |
|--------|------|------|
| **重连机制**：连接建立日志 | 启动 dns-edge，看日志 | ✅ `connected to edgeapi` + `(re)established` |
| **重连机制**：断线优雅降级 | kill edge-api 进程 | ✅ keepalive ~20s 内检测到 IDLE→CONNECTING→TRANSIENT_FAILURE，之后每 10s tick 清晰打印 `UpdateNSNodeStatus failed`/`FindNodeTasks failed`，不崩溃、不影响已缓存数据的 DNS 应答 |
| **重连机制**：自动恢复 | 重启 edge-api | ✅ ~3s 内打印 `(re)established`，之后无更多失败日志 |
| **重连机制**：断线期间在线状态 | 查 `edgeNSNodes.status.isActive` | 断线期间旧值保留（edgeapi 侧超时逻辑决定何时标离线，属于 edgeapi 范畴），重连后下一个 tick 自动重新 `UpdateNSNodeStatus(true)`，确认 DB 回到 `isActive:true` |
| **冷启动全量重同步** | 完全不做任何手动 DB/任务干预，直接重启 dns-edge | ✅ 重启前 `zoneCount:0`（REFUSED），重启后自动恢复到 `zoneCount:3`，`test.local`/`example.com`/`www.example.com`/`sub.mysite.io` 全部正确解析 |
| **NS 模式记录同步**（A 记录） | 已有数据 dig 验证 | ✅ |
| **NS 模式记录同步**（CNAME 记录） | 手动插入 1 条 CNAME 记录 + 触发 `nsRecordChanged` 任务 | ✅ `dig CNAME` 返回记录本身，`dig A` 自动 chase 到目标的 A 记录 |
| **edgeDNSAPI**：ListNSDomains 排序分页 | curl 获取 token → ListNSDomains | ✅ 按 name 字典序稳定返回 |
| **edgeDNSAPI**：FindNSDomainWithName lazy-create | curl 查询不存在的域名 `smoketest.dev` | ✅ 返回有效 domain，dns-edge 内存里新增了这个 zone |
| **edgeDNSAPI**：CreateNSRecord → dig → ListNSRecords | curl 创建 A 记录 | ✅ dig 立即可查，ListNSRecords 返回一致 |
| **edgeDNSAPI**：DeleteNSRecord | curl 删除刚创建的记录 | ✅ dig 变为 NXDOMAIN（zone 的 SOA 还在，语义正确） |
| **EdgeAdmin NS 集群详情页节点数** | curl 登录后拉 `/ns/clusters/cluster?clusterId=1` | ✅ 修复后 `countNodes:1`，节点列表也一直是对的 |

### 4. 测试过程中发现并顺手修复的问题（非本次原计划范围）

1. **NS 模式无冷启动全量重同步**（用户提出，已修复）：见上方"冷启动全量重同步"决策记录。
2. **edgeDNSAPI 分页非确定性**：`ListNSDomains`/`ListNSRecords` 直接遍历 Go map 分页，同一份数据不同次调用顺序可能不同——已加排序（`edgedns_provider.go`）。
3. **EdgeAdmin `/ns/clusters/cluster` 详情页 `countNodes` 未传**：纯前端展示 bug，节点数据本身一直正确——已修复（`edgeadmin` 仓库，`cluster.go`）。

### 5. 已知的环境噪音（非代码 bug，记录以免下次误判）

- 本机 `db_edge` 里 `test.local` 域名重复出现两条记录（`id=1, clusterId=0` 和 `id=2, clusterId=1`），是历史测试遗留的脏数据。两条在 dns-edge 的 ZoneStore 里会撞到同一个 zone apex，导致 `dig test.local` 结果在两个域名的记录间不确定地摆动（`5.6.7.8` / `10.0.0.1`）——这是**真实 DNS 语义下的名字冲突**（一个权威服务器不可能对同一个 apex 名字服务两个不同的"域"），根源是测试数据本身有重复，不是 dns-edge 或 edgeapi 的代码问题。建议后续找机会清理掉 `clusterId=0` 那条孤儿域名。
- `example.com`/`mysite.io` 两个域名最初 `version=0`（2026-07-02 手工插入种子数据时用的是裸 SQL，没走正常的 CreateNSDomain/CreateNSRecord 服务层，没有正确打版本号），导致同步 RPC 的 `version > cursor` 语义永远排除它们。本次测试时已手动把它们的 `version` 和关联记录的 `version` 一起刷新为当前时间戳，验证冷启动重同步能覆盖到它们；这也是数据问题不是代码问题，但值得记一笔，因为它一度掩盖了对冷启动重同步功能本身的验证。

### 6. 待补充（未在本轮测试范围内）

- NS 模式下通过 EdgeAdmin UI 触发的 `DeleteNSRecord`（走 edgeapi 的 `NSRecordService`，不是本次验证的 edgeDNSAPI 直接 CRUD）软删除语义——今天验证 CNAME 清理时误用了裸 SQL `DELETE`（正确做法应该是 soft delete 让 `IsDeleted=true` 被同步下去），已通过重启 dns-edge 让脏数据自然消失，但没有专门走一遍"EdgeAdmin 删记录 → 任务 → dig 确认消失"的正规流程，建议下次找时间补测。

### 7. 追加修复：NS 仪表盘统计为空时前端崩溃（2026-07-03，用户报告触发）

用户反馈 `/ns` 仪表盘"近24小时"图表连坐标轴都没了。根因**不是 CSS 回归**（2026-07-02 修的 `ns/index.css` 容器高度还在），而是统计窗口零命中（`edgeNSRecordHourlyStats` 种子数据停留在 2026-07-02，滑出了"过去24小时"窗口）时，`ComposeNSBoard` 返回的统计切片是 Go nil slice，JSON 序列化成 `null`；`ns/index.js` 对 `hourlyStats`/`topDomainStats` 无条件 `.map()`，遇到 `null` 直接抛异常，把同一个回调里排在后面的"域名排行"图表也一起带崩——这是**任何域名零流量时都会触发的通用 bug**，不是本次种子数据过期特有的。

**踩坑**：先在 `edgeapi`（`service_ns.go`）把 4 个统计切片从 `var x []T` 改成 `var x = []T{}`（非 nil），编译部署后 curl 复测**仍是 `null`**——gRPC/protobuf 的 repeated 字段序列化不区分"空切片"和"未设置"，这个信息过不了 gRPC 边界，edgeadmin 客户端反序列化拿到的永远是 nil。真正的修复点必须在 edgeadmin 自己的 action（`internal/web/actions/default/ns/index.go`，生成 HTTP JSON 响应的那一层）显式初始化非 nil 空切片。

**修复**（`edgeapi`+`edgeadmin` 两个仓库都改了，均已编译重启）：
1. `edgeapi/internal/rpc/services/service_ns.go`：4 个统计切片非 nil 初始化（防御性最佳实践，但过不了 gRPC 边界，非关键修复）。
2. `edgeadmin/internal/web/actions/default/ns/index.go`：同名 4 个本地变量非 nil 初始化——这是真正生效的修复点。

**验证**：curl 复测 `POST /ns`，`hourlyStats`/`topDomainStats`/`topNodeStats` 从 `null` 变成 `[]`（`len=0`）；`dailyStats` 因为还有真实数据（14天）本来就不受影响。详见 `findings.md`「NS 仪表盘"24小时无数据时坐标系整个消失"」一节。
