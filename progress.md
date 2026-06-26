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
