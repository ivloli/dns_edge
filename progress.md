# 会话日志

## 2026-07-07（续五）

### P12 真正修复 + 通配符残留记录踩坑

用户主动问"这个（P12）怎么修复"，读代码后把 `resyncEmptyEdgeDNSProviders` 从"只看整体 zoneCount 是否为0"改成了"zoneCount>0 时也逐个域名核对 dns-edge 实际有的 zone 和该服务商应该服务的域名列表，缺了就单独补推那个域名绑定的集群"，不再是全推或不推两个极端。`go build`/`go vet`/现有单测通过，commit `807d4ce2`，已推送。

顺手踩了一个坑：摘除通配符 CNAME（P13）部署之后，用户发现随便编的子域名（`gd53.cdn.test.node`）居然还能解析出来，一度怀疑修复没生效。查证是**历史遗留数据**——摘掉"生成通配符"的代码只能拦住以后新增的，`test.node` 域名下之前已经推送过的两条旧 `*` CNAME 记录（两个共用该域名的集群各留了一条）还在数据库里，dns-edge 也一直照常在服务它们。手动把两个集群的 `clusterChange` 任务重置触发重新同步后，"多余记录"清理逻辑才把这两条旧通配符记录真正删掉，之后随便编的子域名正确变成 NXDOMAIN，而真实存在的具体记录（比如 `w3.test.node`，一条真实配置的 CNAME）不受影响、正常解析。

## 2026-07-07（续四）

### 部署验证时发现 test-01 集群解析丢失，确认是 P12 变体不是新 bug

三个服务（今天线路匹配+删除同步修复+摘除通配符CNAME）部署到测试环境后，用户测试发现 `g43bb01.cdn.test.node`（`test-01` 集群自己的专属子域名，精确匹配非通配符）也解析不出来了，一开始怀疑是摘除通配符 CNAME 引入的回归。

排查后确认**不是新 bug、也跟摘除通配符无关**：`test.node` 域名同时挂了两个集群（默认集群 + test-01），今天 dns-edge 重启过几次，默认集群因为一直在改动很快重新推送回来了，但 test-01 自己的 DNS 任务最后一次运行是在这几次重启之前，之后一直没有机会重新推送——因为域名整体 zoneCount 从没归零过（默认集群记录一直在），P12 那个"只在整体 zoneCount 为0时才自动恢复"的盲区正好又踩上了，只是这次是"域名下某个集群的子集丢了"，不是"整个域名丢了"。读代码确认过 `doCluster` 清理多余记录时是精确按集群自己的 DNS 名过滤的，不会误删别的集群的记录，排除了"两个集群互删"的猜测。

用 P12 已经验证过的方法恢复（重置 test-01 集群的 `clusterChange` 任务为待处理，等 20 秒 tick），`dig g43bb01.cdn.test.node` 确认恢复正常。详情记在 `task_plan.md` P12 补充里。

## 2026-07-07（续三）

### 摘除自动通配符 CNAME（P13）

部署测试环境时发现集群配了 DNS 后，任意子域名 `dig` 都能解析出集群 IP（一开始以为是 bug）。查代码发现 `edgeapi/internal/tasks/dns_task_executor.go` 里有段"通配符 CNAME"逻辑，只要集群开了 DNS 就无条件自动加 `* CNAME <dnsName>`，用户完全无感知、无法关闭。

用户当场判断这个行为不对——应该由用户自己在"自动设置的CNAME记录"里显式加 `*`，不该由系统背着用户做。查了这段代码的 git 历史，确认**只存在于我们自己这条分支，`origin/test` 官方基线没有**，是团队自己早前加的定制（今天从 stash 恢复代码时才提交进来的）。确认测试环境目前没有域名依赖这个行为后，直接摘掉，跟 `origin/test` 对齐（`git diff origin/test` 这段代码已无差异）。以后如果想要"引导式"的泛域名开关，是个新功能，还没设计，记在 `task_plan.md` P13 里。

## 2026-07-07（续二）

### NS 模式（智能DNS）接入线路匹配，本机开发验证（P11 补完）

部署到测试环境后发现 NS 记录没有线路选择器（P11），深入调研发现根子更深：dns-edge 的 NS 拉取路径（`edgeagent`）压根没有任何 geo/线路匹配逻辑，跟 CDN 模式的成熟能力（ECS + ip2region + 5级降级）完全脱节。

调研后发现范围比想象中小很多——`internal/dns/handler.go` 的 `filterByGeo` 本来就是通用的（只认 `iface.Record.RouteTags`，不分 CDN/NS 来源），`RouteTags` 字段、`ZoneStore` 多记录支持、`pb.NSRecord.NsRoutes` 协议字段都早就有了，只是没人真正用起来。改动集中在三处：edgeapi 的 `convertRecordToPB` 反查线路填充 `NsRoutes` + `Create/UpdateNSRecord` 接上 `routeIds`；edgeadmin 的记录弹窗加线路多选框；dns-edge 把 CDN 模式现成的 `nsRouteCodesToTags` 抽成共享包 `internal/nsroute`，`edgeagent.applyRecord` 拿来给 `RouteTags` 赋值——**查询路径 `handler.go` 完全没有改动**。

本机端到端验证：给同一记录名配两条不同线路（省份+ISP）的记录，`dig +subnet=` 精确匹配、无 ECS 随机、境外 IP 全兜底三种场景全部符合预期，行为跟 CDN 模式已有的 `handler_test.go` 用例一致。只支持内置线路（`country:`/`province:`/`isp:` 前缀），不支持自定义 IP 段/CIDR/地域线路，跟 CDN 模式实际能力对齐。详细设计写进了 `docs/ecs-geo-routing-design.md` 第 9 节。

顺带把这次 `dig` 验证时发现的一个独立问题也修了：NS 记录/域名软删除（`DeleteNSRecord`/`DeleteNSDomain`）只改了 `state`，没有更新 `version` 列，而增量同步（`ListNSRecordsAfterVersion`/`ListNSDomainsAfterVersion`）是按 `version > ?` 过滤的——删除不动 version，等于对增量同步永久隐身，只能靠重启触发全量同步才清掉。两个 DAO 补上 `Set("version", time.Now().UnixNano())`（复用 Create/Update 同款生成方式），本机验证：不重启 dns-edge，纯等 10 秒轮询，删除的记录正确消失、落回通配符兜底。

全程只在本机开发环境验证，**没有碰今天已经部署的贵州/新加坡测试环境**，代码也还没有提交推送。

## 2026-07-07

### 把 feature/ns-dns-edge 升级部署到同事共享的 admin/api 机器

同事已经把 `test` 分支的 GoEdge 分开部署在两台机器上：`edge-admin` 在此前探索过的那台新 Ubuntu 机器（本地库 `edges`，318 张表，实际是孤立/未使用的旧数据），`edge-api` 在远程 AWS 新加坡机器（`aws-sg-web-obs-05`，`/data/go-edge-test/edge-api/`），真正在用的业务库是 `go-edge-test@172.31.43.85:3308`（1 节点/1 集群/1 用户/1 server，WAF 1 策略 11 规则组 17 规则，DNS 服务商 0 条——没找到用户记忆里"已连第三方 DNS 供应商"的数据，可能记错了环境或者还没配）。

已确认 `feature/ns-dns-edge`（edgeapi/edgeadmin）相对同事的 `origin/test` 是严格超集（`test` 领先 0 提交），代码层面不需要再合并。升级前先用 `edge-api upgrade` 对 `go-edge-test` 的克隆库干跑一次，日志只有 2 条 `MODIFY`（字段变宽，非破坏性），没有 `DROP COLUMN`/`TRUNCATE`，确认安全后才对真库操作。

`edge-api`、`edge-admin` 二进制都已换成新版本（先备份旧二进制/`web/`目录，只换二进制不动 `configs/`），数据校验前后一致。

### 部署过程中顺手发现并修复的 5 个真实 bug

全新（0 记录）环境第一次真正把 NS 模块从头用一遍，暴露了一批此前本地开发一直靠测试数据垫底、从没触发过的 bug：

1. **NS 管理页面 nil-slice → JSON `null` → 前端白屏**：`/ns/clusters`、`/ns/domains`、`/ns/routes`、`/ns/plans`、`/ns/settings`、集群详情页节点列表、域名分组下拉、线路分类列表、记录列表、仪表盘 cpu/memory/load 数值——十几处 action 在列表为空时把 Go nil slice 序列化成 `null`，Vue 模板一算 `.length` 就崩。edgeadmin `813364ed`+`e42fcc59`。
2. **`CreateNSCluster` 写空 JSON 到 `accessLog`/`soa` 列**：EdgeAdmin"添加集群"表单只填名称，`accessLogJSON`/`soaJSON` 传空字节，MySQL JSON 列校验直接拒绝（"The document is empty"），集群建不出来。`hosts` 字段没这个问题是因为它总是过一遍 `json.Marshal`（nil → 合法的 `null` 字面量），这两个字段是直接透传原始字节。edgeapi `8ecd7f88`。
3. **NS 域名的 `clusterId` 全链路没打通**：`convertDomainToPB` 从来没设置 `NsCluster` 字段，导致前端拿到的域名永远看不出属于哪个集群；"添加域名"弹窗只有一个隐藏的 `clusterId` 输入框（没有下拉框可选），"修改域名"弹窗压根没有这个字段——域名很容易创建成 `clusterId=0`（不属于任何集群），对应集群的 edgeagent 永远不会去拉它，页面上却显示"保存成功"。edgeapi `980982dd` + edgeadmin `2a1f80ed`（补了下拉框 + 建/改都要求必选集群）。
4. **记录列表永远是空的**：`records/index.go` 调用 `ListNSRecords` 从没传 `Size`，Go 零值 `0` 传到 TeaGo `dbs.Query.Limit(0)` 就是拼出真的 `LIMIT 0`（这个库里 `-1` 才是"不限制"，`0` 是"真的零条"）——不管数据库里有多少条记录，列表永远查不出东西。edgeadmin `918d1f77`。
5. **NS 全部 7 个删除按钮必定 403**：`clusters/domains/domainGroups/plans/records/routes/nodes` 这 7 个 `delete.go` 都声明了 `CSRF *actionutils.CSRF`，但对应的前端删除按钮用的是最简单的"确认框 + `$post`"（`teaweb.confirm(...) + this.$post(action).params(...).refresh()`），根本不带 `csrfToken`——跟代码库里 CDN 那边成熟的同类删除按钮（不要求 CSRF）对比后，去掉了这 7 处的 CSRF 要求，跟现有约定保持一致。edgeadmin `b1b235d8`。

### dns-edge 部署到贵州 admin 机器

`edge-node`（真实 CDN 边缘节点，443 端口）已经在这台机器上跑着，dns-edge 装在 `/data/go-edge/dns-edge/`，用测试端口 `:5300`（DNS）+ `:8080`（CDN 模式 API，先建好凭证但还没接哪个 DNS 服务商用）。

顺手修了一个 dns-edge 自身的缺口：**`geo` 模块配了但本地没有 xdb 文件时无法从零启动下载**——`geo.New()` 在文件不存在时直接返回错误，而 updater（真正会下载文件的那个）只在 `geo.New()` 成功之后才会被构造出来，导致任何全新部署只要没有预先放好 xdb 文件，地理路由就永久禁用、不会自动补上。改成文件缺失时构造一个空 `Router`（`Lookup` 返回零值 `GeoInfo`，不会崩）、updater 照常挂上去，后台异步下载补上——已用真实 GitHub Release 验证过（下载 `v3.16.0`，10.6MB，全程无需重启）。dns-edge `b66b7e4`。

在 EdgeAdmin 里新建了一个 NS 集群 `ns-test-cluster`（id=1）+ 节点 `ns-test-node01`，域名 `coffee.fafa.com`（domainId=1，绑定 `ns-test-cluster`）+ 记录 `@ A 10.0.99.99`，`dig @127.0.0.1 -p 5300 coffee.fafa.com A` 稳定返回 `10.0.99.99`。edgeagent 的断线自动重连（P10 那次修的机制）在今天多次重启 edge-api 期间也确认生效，没人工干预就自动恢复。

### 顺便查清楚的一件事

同事记忆里"已经连了第三方 DNS 供应商"这件事，跟这套环境（`go-edge-test`）对不上——`edgeDNSProviders`/`edgeDNSDomains` 都是 0 行。查过一批容易混淆的表名（`edgeRegionProviders` 381 行、`edgeServerRegionProviderMonthlyStats` 9 行）确认那些是 GoEdge 自带的"区域-运营商"参考字典数据和统计表，跟"接入第三方 DNS 服务商"无关，不是记录被清空，是这个环境本来就还没配过。留给用户去跟同事确认具体是哪个环境。

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
