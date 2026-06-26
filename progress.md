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
