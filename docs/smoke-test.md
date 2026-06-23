# dns-edge 冒烟测试

环境：本机 + PG `172.31.36.140:5432/dns-edge` + Nacos `120.25.74.229:18848`（namespace `scloud-prod`）

实测通过日期：2026-06-23

---

## 1. 启动

```bash
# 首次部署（建表）
./dns-edge -config Corefile --auto-migrate

# 后续正常启动
./dns-edge -config Corefile
```

**预期日志**

```
config loaded  listen=:5300 workers=0 tcp=true
pg connected
pg schema ensured        # 仅 --auto-migrate 时出现
pg load complete  zones=1 records=4 skipped=0
dns-edge running  listen=:5300  api=:8080
DNS/UDP listening  addr=:5300
DNS/TCP listening  addr=:5300
API listening     addr=:8080
```

Nacos 启动时如果 DataID 还未发布，会输出 `WARN nacos: GetConfig failed ... config data not exist`，属正常现象，`ListenConfig` 已注册，推送到来时自动生效。

---

## 2. 存活检查

```bash
curl -s http://localhost:8080/healthz
```

**预期**：`{"status":"ok"}`，HTTP 200

---

## 3. 通过 API 创建测试数据

```bash
# 建域名
curl -sX POST http://localhost:8080/api/v1/domains \
  -H 'Content-Type: application/json' \
  -d '{"name":"example.com."}' | jq .
# 预期: {"id":1,"name":"example.com."}

# A 记录（单条）
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"www.example.com.","type":"A","ttl":300,"value":"1.2.3.4"}' | jq .

# A 记录（两条，带权重，用于分流验证）
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.1","weight":70}' | jq .
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.2","weight":30}' | jq .
```

---

## 4. DNS 功能验证

以下命令均假设实例监听 `127.0.0.1:5300`。

### 4.1 A 记录查询

```bash
dig @127.0.0.1 -p 5300 www.example.com. A +short
```

**预期**：`1.2.3.4`

### 4.2 加权分流（StaticWeightProvider）

```bash
for i in $(seq 1 20); do dig @127.0.0.1 -p 5300 api.example.com. A +short; done | sort | uniq -c
```

**预期**：`10.0.0.1` 约 14 次，`10.0.0.2` 约 6 次（70/30，有统计波动）

### 4.3 NXDOMAIN（名称不存在）

```bash
dig @127.0.0.1 -p 5300 notexist.example.com. A +noall +comments | grep status
```

**预期**：`status: NXDOMAIN`

### 4.4 NODATA（名称存在但类型不匹配）

```bash
dig @127.0.0.1 -p 5300 www.example.com. MX +noall +comments | grep status
```

**预期**：`status: NOERROR`（ANSWER 为空，AUTHORITY 有 SOA）

### 4.5 REFUSED（不在授权范围）

```bash
dig @127.0.0.1 -p 5300 google.com. A +noall +comments | grep status
```

**预期**：`status: REFUSED`

### 4.6 RFC 8482 TypeANY

```bash
dig @127.0.0.1 -p 5300 www.example.com. ANY +short
```

**预期**：`"RFC8482" ""`

### 4.7 EDNS0 协商

```bash
dig @127.0.0.1 -p 5300 www.example.com. A +edns=0 | grep "udp:"
```

**预期**：`udp: 4096`

### 4.8 AXFR Zone Transfer（TCP）

```bash
dig @127.0.0.1 -p 5300 example.com. AXFR +tcp | grep -v "^;"
```

**预期**：SOA → A/A/A → SOA，包含所有域名记录

### 4.9 TCP 查询

```bash
dig @127.0.0.1 -p 5300 www.example.com. A +tcp +short
```

**预期**：`1.2.3.4`

---

## 5. 热更新验证

```bash
# 添加记录，保存 ID
ID=$(curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"hottest.example.com.","type":"A","ttl":5,"value":"10.0.1.1"}' | jq -r .id)

# 查初始值
dig @127.0.0.1 -p 5300 hottest.example.com. A +short
# 预期: 10.0.1.1

# 热更新
curl -sX PUT http://localhost:8080/api/v1/domains/example.com./records/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"hottest.example.com.","type":"A","ttl":5,"value":"10.0.1.99"}'

# 立即查询（无需等 TTL 过期）
dig @127.0.0.1 -p 5300 hottest.example.com. A +short
# 预期: 10.0.1.99（变更立即生效）
```

---

## 6. Nacos 动态权重推送

通过 Nacos HTTP API 发布权重配置，验证 dns-edge 实时接收并更新分流比例。

```bash
# 1. 登录拿 accessToken
TOKEN=$(curl -s -X POST "http://120.25.74.229:18848/nacos/v1/auth/login" \
  -d "username=scloud-admin&password=<password>" | jq -r .accessToken)

# 2. 发布权重（改为 90/10）
curl -s -X POST "http://120.25.74.229:18848/nacos/v1/cs/configs?accessToken=$TOKEN" \
  --data-urlencode "dataId=dns_weights:api.example.com.:A" \
  --data-urlencode "group=dns_edge" \
  --data-urlencode "tenant=scloud-prod" \
  --data-urlencode 'content={"10.0.0.1":90,"10.0.0.2":10}'
# 预期: true

sleep 2

# 3. 验证分流变化（20次，预期约 18/2）
for i in $(seq 1 20); do dig @127.0.0.1 -p 5300 api.example.com. A +short; done | sort | uniq -c
```

**日志确认**（dns-edge 进程）：
```
INFO  nacos: weights updated  dataId=dns_weights:api.example.com.:A  entries=2
```

**Nacos Web UI 路径**：`http://120.25.74.229:18848/nacos/` → 配置管理 → 命名空间 `scloud-prod` → Group `dns_edge` → DataID `dns_weights:api.example.com.:A`

---

## 7. Prometheus 指标

```bash
curl -s http://localhost:8080/metrics | grep -E '^dns_'
```

**预期关键指标**：

| 指标 | 说明 |
|------|------|
| `dns_queries_total{qtype="A",rcode="NOERROR"}` | A 查询成功计数 |
| `dns_queries_total{qtype="A",rcode="NXDOMAIN"}` | NXDOMAIN 计数 |
| `dns_queries_total{qtype="A",rcode="REFUSED"}` | REFUSED 计数 |
| `dns_query_duration_seconds_bucket{qtype="A",le="5e-05"}` | 大部分 A 查询 < 50µs |
| `dns_sync_total{result="success"}` | 增量同步成功次数 |
| `dns_sync_last_success_timestamp_seconds` | 最近一次同步时间戳 |

---

## 8. 冒烟测试检查单

| 项目 | 命令 | 预期结果 | 实测 |
|------|------|----------|------|
| 进程启动 | `curl /healthz` | `{"status":"ok"}` | ✅ |
| A 记录 | `dig www.example.com. A` | `1.2.3.4` | ✅ |
| 加权分流 | 20 次 dig api | ~14/6（70/30）| ✅ |
| NXDOMAIN | `dig notexist... A` | `status: NXDOMAIN` | ✅ |
| NODATA | `dig www... MX` | `status: NOERROR`，ANSWER 空 | ✅ |
| REFUSED | `dig google.com.` | `status: REFUSED` | ✅ |
| RFC 8482 ANY | `dig www... ANY` | `"RFC8482" ""` | ✅ |
| EDNS0 | `dig +edns=0` | OPT `udp: 4096` | ✅ |
| AXFR TCP | `dig ... AXFR +tcp` | SOA→记录→SOA | ✅ |
| 热更新 | PUT 记录后立即 dig | 新值立即生效 | ✅ |
| Nacos 权重推送 | 发布 DataID 后 dig | 分流比例实时更新 | ✅ |
| 增量同步 | 等 30s 后查 metrics | `dns_sync_total{result="success"} ≥ 1` | ✅ |
| Prometheus | `curl /metrics` | dns_* 指标均有数据 | ✅ |
| 多类型 metrics | dig ANY/MX 后查 metrics | qtype 标签涵盖多种类型 | ✅ |
