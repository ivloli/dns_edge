# 会话日志

## 2026-07-07

### 把 feature/ns-dns-edge 升级部署到同事共享的 admin/api 机器

同事已经把 `test` 分支的 GoEdge 分开部署在两台机器上：`edge-admin` 在此前探索过的那台新 Ubuntu 机器（本地库 `edges`，318 张表，实际是孤立/未使用的旧数据），`edge-api` 在远程 AWS 新加坡机器（`aws-sg-web-obs-05`，`/data/go-edge-test/edge-api/`），真正在用的业务库是 `go-edge-test@172.31.43.85:3308`（1 节点/1 集群/1 用户/1 server，WAF 1 策略 11 规则组 17 规则，DNS 服务商 0 条——没找到用户记忆里"已连第三方 DNS 供应商"的数据，可能记错了环境或者还没配）。

已确认 `feature/ns-dns-edge`（edgeapi/edgeadmin）相对同事的 `origin/test` 是严格超集（`test` 领先 0 提交），代码层面不需要再合并。升级前先用 `edge-api upgrade` 对 `go-edge-test` 的克隆库干跑一次，日志只有 2 条 `MODIFY`（字段变宽，非破坏性），没有 `DROP COLUMN`/`TRUNCATE`，确认安全后才对真库操作。

`edge-api`、`edge-admin` 二进制都已换成新版本（先备份旧二进制/`web/`目录，只换二进制不动 `configs/`），数据校验前后一致。过程中发现一个真实 bug：**`/ns/clusters`、`/ns/domains` 等 5 个 NS 管理页面在列表为空时把 Go nil slice 序列化成 JSON `null`**，导致 Vue 模板渲染崩溃、页面白屏——本地开发一直有测试数据垫底从未暴露，这次是第一次部署到全新（0 记录）环境才踩到。已修复 `edgeadmin` 5 处 action（`ns/clusters`、`ns/domains`、`ns/routes`、`ns/plans`、`ns/settings` 的 `index.go`），改为显式空切片初始化，重新打包待部署。

## 2026-07-06（续）

### 全新机器源码编译探索 + 发现同事共享环境 + 排查 fafa.com/momo.com 记录"丢失"

用户打算在一台新 Ubuntu 机器上试源码编译（给了 SSH key 生成 + Go 1.25.11 安装命令）。排查这台机器时发现已经有同事部署了 `test` 分支的 GoEdge（`/data/go-edge/edge-admin` + `edge-node`），且 `edge-admin` 连的是**远程** `47.129.241.108:8001`（AWS 新加坡区域 EC2，查不到具体是谁的账号）——真正的 edgeapi 根本不在这台机器上。确认后决定：不碰 `/data/go-edge/`，用独立目录/独立数据库名/独立端口在同一台机器上部署我们自己这套（详细步骤见对话，未来接手时可参考）。用户提出"现在就分开以后合并代价更大"，讨论后结论：代码已经是 test 的超集所以合并本身不难，但这台机器的 edge-admin 并非真正的控制面，贸然改配置等于切走一个可能带着真实流量的系统的管理入口，风险和收益不对等，还是先隔离验证。

在这台开发机上排查另一件事：用户发现 `dig fafa.com`/`momo.com` REFUSED。核实 GoEdge 侧数据库（`edgeDNSDomains`）记录完好无损，只是 dns-edge 内存里这两个 zone 因为今天反复重启而丢了，且 edgeapi 的"自动恢复"机制只在**整体** zoneCount 归零时才触发重推，个别域名单独掉线不会被发现——手动把对应 `edgeDNSTasks` 的 `clusterChange` 任务重置成待处理后成功恢复。这个盲区已记入 `task_plan.md` P12。

顺手在数据库里给所有集群（CDN + NS）补了几个节点（`isInstalled=1`），并给 7 条现有 NS 记录都加上了"国家+省份+ISP"线路（`edgeNSRecords.routeIds` 引用 `edgeNSRoutes`）。过程中发现一个真实缺口：**NS 记录的创建/编辑弹窗根本没有线路选择器，且 `CreateNSRecord`/`UpdateNSRecord` 这两个 RPC 硬编码把 routeIds 传 `nil`**——线路只能直接改数据库设置，页面走不通。已记入 `task_plan.md` P11。

---

### 把 NS/DNS 整套改动迁移到 test 环境分支（4 个仓库）

用户要求把这几天在 dns_dev/edgeapi/edgeadmin 的 NS 改动挪到团队的 `test` 环境分支，出 Makefile + 一键部署脚本。排查过程中发现 `edgeadmin` 的 NS 代码依赖 `edgecommon` 仓库自己新加的 pb 类型，实际要迁移的是 **4 个仓库**（多了 edgecommon）。

**踩的坑（关键）**：`test` 分支所在的团队主线在我们的 feature 分支分叉之后做过一次模块改名——`test` 用 `gitlab.gainetics.io/backend-cdn/goedge/edgecommon`（`replace ../edgecommon` 指向本地私有 fork），我们的 feature 分支用 `github.com/TeaOSLab/EdgeCommon`（一个从公共 proxy.golang.org 能下载到的**开源公版**）。`git merge` 时自动收敛到了公版命名，编译时才暴露问题——同事在 `test` 分支给 WAF 黑白名单加的 `HTTPAccessLog.FirewallListId`/`FirewallListType` 字段公版里没有。用户两次打断确认"是不是把同事工作覆盖了"，核实后改为让 edgecommon 仓库自身的模块名/内部 121 处 import 路径统一改回 `gitlab.gainetics.io/...`（纯路径重命名，不改逻辑），edgeapi（244 处）/edgeadmin（31 处）里同名的引用一并改回来，而不是反过来动 test 那边。

**分支**：4 个仓库统一建 `feature/ns-dns-edge`——edgecommon/edgeapi/edgeadmin 从 `origin/test` 拉出（edgeapi 是真 merge + 1 处无关小冲突；edgeadmin 是 `main` 从未提交过 NS 代码，直接从 test 分支重新应用），dns_dev 从 `master` 拉出（fast-forward，无冲突）。过程中还发现 edgeapi 有一批 NS service（`service_ns.go`/`service_ns_cluster.go` 等 7 个文件 + 3 个 DAO + installer）一直只在 `git stash` 里没提交，忘了 pop 差点当成"合并已完成"——git stash 没丢东西，补 pop 后修好 import 路径重新提交。

**Makefile + 部署脚本**：三个仓库（edgeapi/edgeadmin/dns-edge）各自 Makefile 加了 `build`/`run-local`/`stop-local`/`restart-local`/`deploy-local`/`status-local`（PID 文件+nohup，这台机器没有免密 sudo 跑不了 systemd）+ `package`（打包成二进制+配置模板的 tarball，不含真实凭证）。`dns_dev/scripts/deploy-test-env.sh`（本机一键部署，依赖检查+分支检查+按 edgeapi→edgeadmin→dns-edge 顺序部署+烟测）和 `scripts/package-release.sh`（打包三个 tarball，供拷到全新机器——用户后来明确要部署到全新机器，讨论后选打包而非源码编译，因为源码编译要在新机器上重建今天踩的这堆本地依赖坑）都已经端到端跑通验证。

**最终核对**：三个依赖 edgecommon 的仓库（edgecommon 自己/edgeapi/edgeadmin）的模块命名、`go.mod` 依赖声明都已经和 `origin/test` 逐项比对一致，`go build`/`CGO_ENABLED=0 go build` 全部干净，四个仓库都没有推送到远程。

---

## 2026-07-03

### edgeagent 重连机制补齐（P10）

代码复查发现用户记忆中"已完成"的重连机制实际未做（只有 gRPC channel 默认行为兜底，无 supervise、无心跳恢复），CNAME/记录同步机制本身是对的（通用 RR 解析，无需特殊处理）。

修复 `internal/edgeagent/agent.go`：初始连接失败指数退避重试、gRPC keepalive 加快断线探测、连接状态变化日志、在线状态从"启动时一次"改为随 10s ticker 周期上报（重连后自动恢复在线状态）、`ctx.Done()` 时优雅上报离线。纯边缘节点侧改动，不涉及 edgeapi。

验证：`go build ./...` / `go vet` / `gofmt -l` 干净；`go test ./...` 唯一失败 `TestEdgeDNS_ListDomains` 经 stash 对比确认是改动前已存在的问题，与本次无关。

---

### 冷启动全量重同步 + edgedns_test.go 大修 + 端到端测试报告

修 `go test` 时顺带发现 `edgedns_test.go` 近一半测试（12个）在 mock 一个生产代码早就不读的依赖（`no-PG` 迁移后 `edgedns_provider.go` 只读写 ZoneStore），改用真实 `store.RWMutexStore` 重写，过程中顺带修了一个生产 bug：`ListNSDomains`/`ListNSRecords` 分页因 map 遍历非确定性导致结果不稳定（加排序）。

编译重启本地三个服务后做端到端测试，发现真实问题：dns-edge 重启后 NS 域名全部 REFUSED——根因是 edgeagent 只在收到 `nsDomainChanged`/`nsRecordChanged` 任务时才同步，任务是一次性的，重启后若已消费完就再也不会主动全量拉取（CDN 模式有 edgeapi 侧 zoneCount 自动恢复，NS 模式没有对应机制）。已在 `agent.go` 里加上"连接建立后无条件全量 sync"，验证：真实冷重启（无任何手动 DB 干预）后 zoneCount 从 0 自动恢复到 3，全部域名正确解析。

测试过程中用户反馈 EdgeAdmin `/ns/clusters/cluster?clusterId=1` 详情页节点数为空，排查发现是纯前端 bug（`cluster.go` 没传 `countNodes` 字段给模板），跟本次同步改动无关，顺手修了（`edgeadmin` 仓库，不同代码库）。

完整测试报告已写入 `task_plan.md`「测试报告（2026-07-03）」一节，含单测结果、编译重启记录、11 项 NS 功能端到端验证、3 个顺手修复的问题、2 条环境噪音说明（历史脏数据导致的非代码问题）。

---

### NS 仪表盘统计为空导致前端崩溃（用户报告后追加修复）

用户反馈 `/ns` 仪表盘"近24小时"图表连坐标轴都没了。排查确认不是 CSS 回归，是统计窗口零命中时后端返回 `null`（Go nil slice），前端 `.map()` 无条件调用直接崩溃，把同一批的域名排行图也带崩——任何域名零流量都会触发，是通用 bug 不是本次种子数据过期特有。

踩坑：先以为在 edgeapi 侧把切片初始化成非 nil 就够了，编译部署后复测仍是 `null`——gRPC/protobuf repeated 字段序列化不区分空切片和未设置，这个信息过不了 gRPC 边界。真正修复点是 edgeadmin 自己生成 HTTP JSON 的那层（`ns/index.go`）。两个仓库都改了（edgeapi 那处算防御性最佳实践，非关键；edgeadmin 那处才是真正生效的），均已编译重启，curl 复测 `hourlyStats`/`topDomainStats`/`topNodeStats` 从 `null` 变成 `[]`。详见 `findings.md`。

---

## 2026-07-01

### NS 模式端到端联调完成

**环境修复**：
- `Tea.Root` 规则导致 edgeapi 读 `/home/ivloli/configs/` 而非 `edgeapi-run/configs/`
- `/home/ivloli/configs/db.yaml` 改为 `db_edge`，`api.yaml` 改为 db_edge id=1 的凭据（gRPC :8031）
- 删除废弃的 `edgeapi-run/edge-api-comm` 和 `edge-api-comm.bak`

**代码修复**：

| 修复 | 文件 | 说明 |
|------|------|------|
| convertRecordToPB 填充 NsDomain | edgeapi `service_ns_record.go` | agent 侧需要 zone 名展开相对记录名 |
| applyRecord 记录名展开 | dns-edge `internal/edgeagent/agent.go` | `@`→apex，`name`→`name.zone.`，覆盖所有相对标签 |

**验证结果**：

| 测试 | 结果 |
|------|------|
| `dig @127.0.0.1 -p 5300 test.local A` | `10.0.0.1` ✅ |
| `dig @127.0.0.1 -p 5300 www.test.local A` | `1.2.3.4` ✅ |
| nsDomainChanged 任务消费 | isDone=1 isOk=1 ✅ |
| nsRecordChanged 任务消费 | isDone=1 isOk=1 ✅ |

**当前运行状态**：
- edgeapi: pid 624354，`/home/ivloli/edgeapi-run/edge-api`，gRPC :8031，db_edge
- dns-edge: `./dns-edge-local -config Corefile.local`，DNS :5300，edgeagent → 127.0.0.1:8031

---

## 2026-06-30

### NS 模块合并 + gRPC 鉴权修复

将 edgeapi-comm（商业版）NS 代码合并到 edgeapi（feature/ivloli），统一使用 db_edge：

**合并内容**：
- `internal/db/models/nameservers/*.go`（30 个文件）
- `internal/rpc/services/service_ns_*.go`（node/cluster/domain/record/route）
- `internal/nodes/api_node_services.go`（注册 NS gRPC 服务）
- `internal/db/models/node_task_dao_ext.go`（实现 ExtractNSClusterTask，之前是空 stub）

**鉴权修复**：
1. `ns_node_dao.go CreateNSNode`：补加 `SharedApiTokenDAO.CreateAPIToken(tx, uniqueId, secret, NodeRoleDNS)`
2. `utils_ext.go ValidateRequest`：switch 补 `UserTypeDNS` case
3. `service_ns_node.go` 节点侧方法：改用 `ValidateNodeId(ctx, UserTypeDNS)`

**dns-edge edgeagent 新增**：
- `internal/edgeagent/agent.go`：gRPC 连接 + AES-256-CFB 鉴权 + 任务轮询 + domain/record 增量同步
- `Corefile.local`：新增 edgeagent 块，endpoint 127.0.0.1:8031

---

## 2026-06-29

### CDN 功能完善

1. **动态权重**：edgeapi 按节点 load1m 计算 Weight（`min(100, max(1, 100/load1m))`），dns-edge weightedRandom
2. **通配符 CNAME**：edgeapi doCluster 自动推 `* CNAME cluster1.<domain>`，dns-edge wildcardLookup
3. **Bug 修复**：toNSRecordObj 尾点残留（`*.fafa.com.` → `*.`）；findClusterDNSChanges 缺少 `*` 保护

---

## 2026-06-27

### CDN 基础功能

1. zoneCount /healthz + O(1) 空检测
2. ClusterNodesChange → ClusterChange（CNAME 恢复）
3. filterByGeo IP 聚合
4. xdb 自动更新（GitHub Releases 热替换）
5. geo parseRegion 4/5 字段兼容
