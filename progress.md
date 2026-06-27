# 会话日志

## 2026-06-26 — 联调完成

### 完成的工作

1. **重写 `edgedns_provider.go`**：彻底去掉 `s.pg` 依赖，所有 edgeDNSAPI 端点改为操作 `s.store`（ZoneStore）
   - Zone ID：FNV-1a 64-bit hash of apex FQDN（纯内存，可重复）
   - Record ID：进程内原子计数器
   - `FindNSDomainWithName`：zone 不存在时 lazy-create 空 zone
   - `CreateNSRecord`：分配 ID，调用 `store.PutRecord()`，DNS 立即生效
   - `UpdateNSRecord`：找到 apex，`store.PutRecord()` 覆盖（ID 匹配）
   - `DeleteNSRecord`：找到 apex，`store.DropRecord()`
   - 全部辅助函数（resolveApexByID、findApexForRecord、listRecordsInZone、queryByNameType）

2. **联调验证通过**：
   ```
   getAPIAccessToken → code 200, token 获取成功
   FindNSDomainWithName("test.local") → lazy-create zone，返回 domainId
   CreateNSRecord(www.test.local A 10.0.0.1) → nsRecordId: 1
   ListNSRecords → 返回已创建记录
   dig @127.0.0.1 -p 5300 www.test.local A → 10.0.0.1 ✓
   ```

3. **更新规划文档**：task_plan.md / progress.md / findings.md 重写，去掉历史废弃内容

### 关键结论

- no-PG 模式完全可用：启动 Corefile.local（仅配置 api.listen + edgedns_access_key），edgeDNSAPI 全部工作
- GoEdge 不调用 CreateNSDomain，FindNSDomainWithName 需要 lazy-create zone
- dns-edge 重启后记录丢失是预期行为（GoEdge 会重新推送）

### 下一步

- P1：在 EdgeAdmin 配置 edgeDNSAPI provider，通过 GoEdge UI 触发域名+记录创建，验证 dig

## 2026-06-26 — 全链路联调 + 自动恢复

### 完成的工作

1. **EdgeAdmin UI 全链路联调通过**：
   - EdgeAdmin → DNS 管理 → 新建 edgeDNSAPI provider → 新建域名 `edge-test.local`
   - 绑定集群 DNS，设置二级域名前缀 `cluster1`
   - 点「同步」→ edgeapi 调用 `FindNSDomainWithName` lazy-create zone，再 `CreateNSRecord` 推送节点 IP
   - `dig @127.0.0.1 -p 5300 cluster1.edge-test.local A` → `10.100.0.1` ✓

2. **自动恢复机制（edgeapi 侧）**：
   - `edgeapi/internal/tasks/dns_task_executor.go` 新增 `resyncEmptyEdgeDNSProviders()`
   - 每 20s tick 调 `GetDomains()`，若 dns-edge 返回空列表（重启后），立即插 `ClusterNodesChange` task
   - 实测：dns-edge 重启后 **5 秒内** `dig` 返回正确记录，无需手动同步 ✓

3. **部署文档重写**：`setup_guide.md` 全量重写，覆盖 MySQL 初始化→编译→配置→EdgeAdmin 绑定→systemd

4. **文档清理**：task_plan.md / findings.md / progress.md 更新至当前状态

### 关键发现

- dns-edge 无法反向调 edgeapi（gRPC + 身份认证壁垒），自动恢复只能在 edgeapi 侧实现
- NS 系列服务（NSDomainService 等）走 edgeapi 的 `RestServer`（HTTP），不走 gRPC；dns-edge edgeDNSAPI server 正是这套协议的 server 端
- 商业版只有 client（`provider_edge_dns_api.go`），server 端由我们实现

### 当前各组件状态

| 组件 | 状态 |
|------|------|
| dns-edge | 运行中（:5300 DNS + :8080 API） |
| edgeapi | 运行中（:8031 gRPC），含新自动恢复逻辑 |
| EdgeAdmin | 运行中（:7788） |

## 2026-06-27 — ECS 地理路由验证 + xdb 自动更新

### 完成的工作

1. **修复 geo parseRegion 字段索引**：
   - 原代码按 5 字段格式（含「区域」）解析，实际 xdb 是 4 字段（`国家|省份|城市|ISP`）
   - 修正字段索引，Province=parts[1]，ISP=parts[3]
   - 新增 `normalizeProvince`（去掉「省」「市」）和 `normalizeISP`（去掉「中国」「云」）
   - 参照 `/home/ivloli/Git_repo/dns/plugin/ecs_normalizer/util.go` 中的规范化逻辑

2. **parseRegion 兼容多版本 xdb**：
   - 4 字段（旧版）和 5 字段（新版 v3.x，含 CC 或区域=0）均正确解析

3. **ECS 地理路由 5 场景全部验证通过**：
   ```
   浙江电信 122.224.0.1 → 3.3.3.3  (province+ISP 精确)  ✓
   浙江移动 111.0.0.1   → 1.1.1.1  (province 匹配)       ✓
   广东移动 183.232.0.1 → 2.2.2.2  (province 匹配)       ✓
   北京联通 123.125.0.1 → 9.9.9.9  (默认)                ✓
   无 ECS              → 随机      (clientIP=nil)         ✓
   ```

4. **ip2region xdb 自动更新**（`internal/geo/updater.go`）：
   - 启动时后台检查 GitHub Releases，版本不同则下载新 xdb
   - 热替换：原子 rename + `Router.swap()`，无需重启
   - 定时 24h 检查，可配置 interval 和 github_token
   - 实测：删除版本标记文件 → 重启 → 1 秒内下载并替换 v3.16.0 ✓

### 新增文件

- `internal/geo/updater.go` — xdb 自动更新器

### 修改文件

- `internal/geo/geo.go` — parseRegion/normalizeProvince/normalizeISP/Router.swap()
- `config/config.go` — GeoConfig 新增 AutoUpdate/UpdateInterval/GithubToken
- `config/parser.go` — 解析 geo 块新字段
- `cmd/dns-edge/main.go` — 接入 Updater
- `Corefile.local` — 启用 auto_update true, update_interval 24h
