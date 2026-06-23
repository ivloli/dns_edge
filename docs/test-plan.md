# dns-edge 测试文档

## 1. 测试范围与分层

| 层级 | 工具 | 覆盖目标 | 需要外部依赖 |
|------|------|----------|-------------|
| 单元测试 | `go test` | 各包逻辑正确性 | 否（Mock） |
| 基准测试 | `go test -bench` | 热路径性能 | 否（Mock） |
| API 集成测试 | `curl` | HTTP CRUD 端到端 | 需要运行中的实例 + PG |
| DNS 功能测试 | `dig` / `kdig` | DNS 协议行为 | 需要运行中的实例 |
| 观测性验证 | `curl` | `/healthz` / `/metrics` | 需要运行中的实例 |
| 压力测试 | `dnsperf` / `flamethrower` | QPS 上限 & 延迟分布 | 需要运行中的实例 |

---

## 2. 单元测试

### 2.1 运行所有测试

```bash
# 标准运行（含竞态检测，推荐）
go test -race -count=1 ./...

# 带详细输出
go test -race -count=1 -v ./...

# 指定包
go test -race -count=1 ./internal/dns/...
```

**预期结果**

```
ok  dns-edge/config              0.007s
ok  dns-edge/internal/api        0.023s
ok  dns-edge/internal/dns        0.013s
ok  dns-edge/internal/store      0.010s
ok  dns-edge/internal/syncer     0.017s
```

所有包 `ok`，无 `FAIL`，无 data race 报告。

### 2.2 测试覆盖范围

| 包 | 测试文件 | 用例数 | 关键场景 |
|----|---------|--------|---------|
| `config` | `parser_test.go` | 19 | 默认值、全字段、块嵌套、注释、错误路径 |
| `internal/store` | `rwmutex_test.go` | 20 | PutRecord/DropRecord/NameExists/FindZone/Snapshot/COW 不变性 |
| `internal/dns` | `handler_test.go` | 15 | A/MX 查询、NXDOMAIN、NODATA、REFUSED、CNAME 追踪、TypeANY、AXFR TCP/UDP、syntheticSOA、概率触发 |
| `internal/syncer` | `syncer_test.go` | 8 | tokenBucket 限流/时间前进/容量上限、TriggerSync 非阻塞、doSync 调用链 |
| `internal/api` | `api_test.go` | 15 | 域名/记录 CRUD 全路径、Conflict/NotFound 错误码、`/healthz` |

---

## 3. 基准测试

```bash
# 运行全部基准，每项 3s
go test -bench=. -benchmem -benchtime=3s ./...

# 只跑 DNS handler
go test -bench=. -benchmem -benchtime=3s ./internal/dns/...

# 只跑 ZoneStore
go test -bench=. -benchmem -benchtime=3s ./internal/store/...

# 并发 A 查询（模拟真实负载）
go test -bench=BenchmarkServeDNS_A_Parallel -benchmem -cpu=1,2,4,8 ./internal/dns/...
```

**参考基准值（amd64 2.5 GHz Xeon，Go 1.25）**

| 场景 | ns/op | allocs/op | 备注 |
|------|-------|-----------|------|
| A 查询（MockStore） | ~317 | 5 | 无锁基线 |
| A 查询（RealStore，含 RWMutex）| ~502 | 5 | 生产路径 |
| NXDOMAIN（NameExists + SOA 合成）| ~556 | 8 | 负向应答 |
| CNAME 追踪（2 次 Lookup） | ~437 | 7 | 单跳 |
| TypeANY → HINFO | ~351 | 5 | RFC 8482 |
| 并发 A 查询（8 GOMAXPROCS） | ~434 | 5 | 读锁伸缩 |
| AXFR（TCP，全记录序列化）| ~8229 | 16 | Zone 传输 |
| Lookup 纯读路径 | ~85 | **0** | 零分配 |
| PutRecord COW 替换 | ~2622 | 19 | 热更新路径 |

> `allocs/op = 0` 表示读路径对 GC 完全无压力。

---

## 4. DNS 功能测试

以下命令假设实例监听 `127.0.0.1:5300`，测试区域为 `example.com.`。
用 `dig @127.0.0.1 -p 5300` 替换为实际地址。

### 4.1 环境准备

启动实例（无 PG 时使用 seed zone 模式）：

```bash
./dns-edge -config Corefile
```

或带 PG 预加载：

```bash
./dns-edge -config Corefile --auto-migrate
```

预置测试数据（以下 curl 命令假设 API 在 `:8080`）：

```bash
# 创建区域
curl -sX POST http://localhost:8080/api/v1/domains \
  -H 'Content-Type: application/json' \
  -d '{"name":"example.com."}'

# A 记录（单条）
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"www.example.com.","type":"A","ttl":300,"value":"1.2.3.4"}'

# A 记录（两条，用于分流验证）
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.1","weight":70}'
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.2","weight":30}'

# CNAME
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"blog.example.com.","type":"CNAME","ttl":300,"value":"www.example.com."}'

# MX
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"example.com.","type":"MX","ttl":300,"value":"10 mail.example.com."}'

# TXT
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"example.com.","type":"TXT","ttl":300,"value":"v=spf1 -all"}'
```

### 4.2 A 记录查询

```bash
dig @127.0.0.1 -p 5300 www.example.com. A
```

**预期**
- `status: NOERROR`
- `ANSWER SECTION` 包含 `1.2.3.4`
- `aa` 标志（Authoritative Answer）为真
- TTL = 300

### 4.3 加权分流验证

```bash
# 多次查询，观察返回 IP 分布
for i in $(seq 1 20); do
  dig @127.0.0.1 -p 5300 api.example.com. A +short
done
```

**预期**：`10.0.0.1` 出现约 14 次，`10.0.0.2` 约 6 次（70/30 权重，有统计波动）。

### 4.4 CNAME 追踪

```bash
dig @127.0.0.1 -p 5300 blog.example.com. A
```

**预期**
- `ANSWER SECTION` 第一条为 CNAME `www.example.com.`
- 第二条为 A `1.2.3.4`（追踪结果）
- `status: NOERROR`

### 4.5 MX 记录

```bash
dig @127.0.0.1 -p 5300 example.com. MX
```

**预期**：`ANSWER SECTION` 包含优先级 + 主机名，如 `10 mail.example.com.`

### 4.6 TXT 记录

```bash
dig @127.0.0.1 -p 5300 example.com. TXT
```

**预期**：`ANSWER SECTION` 包含 `"v=spf1 -all"`

### 4.7 NXDOMAIN（名称不存在）

```bash
dig @127.0.0.1 -p 5300 nonexistent.example.com. A
```

**预期**
- `status: NXDOMAIN`
- `AUTHORITY SECTION` 包含 SOA 记录

### 4.8 NODATA（名称存在但类型不匹配）

```bash
# www.example.com. 只有 A，查 MX 应返回 NODATA
dig @127.0.0.1 -p 5300 www.example.com. MX
```

**预期**
- `status: NOERROR`
- `ANSWER SECTION` 为空
- `AUTHORITY SECTION` 包含 SOA 记录

### 4.9 REFUSED（不在授权范围内）

```bash
dig @127.0.0.1 -p 5300 notmydomain.com. A
```

**预期**：`status: REFUSED`

### 4.10 RFC 8482（TypeANY）

```bash
dig @127.0.0.1 -p 5300 www.example.com. ANY
```

**预期**
- `status: NOERROR`
- `ANSWER SECTION` 包含 `HINFO "RFC8482" ""`

### 4.11 EDNS0 协商

```bash
dig @127.0.0.1 -p 5300 www.example.com. A +edns=0
```

**预期**：响应 `ADDITIONAL SECTION` 包含 OPT 记录，`udp: 4096`

```bash
# 不带 EDNS0 的客户端，响应中不应有 OPT
dig @127.0.0.1 -p 5300 www.example.com. A +noedns
```

**预期**：响应无 OPT 记录（`ADDITIONAL SECTION` 为空）

### 4.12 AXFR Zone Transfer

```bash
# AXFR 需要 TCP
dig @127.0.0.1 -p 5300 example.com. AXFR +tcp
```

**预期**
- 第一条和最后一条均为 SOA
- 中间包含区域内所有记录
- 传输无错误

```bash
# UDP 上的 AXFR 应被拒绝
dig @127.0.0.1 -p 5300 example.com. AXFR
```

**预期**：`status: REFUSED`

### 4.13 TCP 查询

```bash
dig @127.0.0.1 -p 5300 www.example.com. A +tcp
```

**预期**：与 UDP 查询相同的 `NOERROR` 结果（TCP 支持默认开启）

---

## 5. HTTP API 测试

### 5.1 健康检查

```bash
curl -s http://localhost:8080/healthz
```

**预期**：`HTTP 200`，响应体 `{"status":"ok"}`

### 5.2 Prometheus 指标

```bash
curl -s http://localhost:8080/metrics | grep dns_queries_total
```

**预期**：输出类似：

```
# HELP dns_queries_total Total DNS queries received, by qtype and rcode.
# TYPE dns_queries_total counter
dns_queries_total{qtype="A",rcode="NOERROR"} 42
```

### 5.3 域名 CRUD

```bash
# 创建
curl -sX POST http://localhost:8080/api/v1/domains \
  -H 'Content-Type: application/json' \
  -d '{"name":"test.example."}' | jq .
# 预期: 201 Created，返回 zone 对象

# 列出
curl -s http://localhost:8080/api/v1/domains | jq .
# 预期: 200，包含刚创建的域名

# 重复创建（冲突）
curl -sw '\nHTTP %{http_code}\n' -X POST http://localhost:8080/api/v1/domains \
  -H 'Content-Type: application/json' \
  -d '{"name":"test.example."}'
# 预期: HTTP 409 Conflict

# 删除
curl -sw '\nHTTP %{http_code}\n' -X DELETE \
  http://localhost:8080/api/v1/domains/test.example.
# 预期: HTTP 204 No Content

# 删除不存在的域名
curl -sw '\nHTTP %{http_code}\n' -X DELETE \
  http://localhost:8080/api/v1/domains/ghost.example.
# 预期: HTTP 404 Not Found
```

### 5.4 记录 CRUD

```bash
# 添加记录
REC=$(curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":60,"value":"1.2.3.4"}')
echo $REC | jq .
ID=$(echo $REC | jq -r .id)

# 列出记录
curl -s http://localhost:8080/api/v1/domains/example.com./records | jq .

# 更新记录
curl -sX PUT http://localhost:8080/api/v1/domains/example.com./records/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":120,"value":"5.6.7.8"}' | jq .
# 预期: 200，TTL 更新为 120，value 更新为 5.6.7.8

# 验证 DNS 查询立即生效
dig @127.0.0.1 -p 5300 api.example.com. A +short
# 预期: 5.6.7.8（热更新生效）

# 删除记录
curl -sw '\nHTTP %{http_code}\n' -X DELETE \
  http://localhost:8080/api/v1/domains/example.com./records/$ID
# 预期: HTTP 204 No Content

# 验证删除生效（应返回 NXDOMAIN 或 NODATA）
dig @127.0.0.1 -p 5300 api.example.com. A
```

---

## 6. 热更新一致性测试

验证 API 写入后 DNS 查询立即能看到最新值（同实例内）。

```bash
# 初始值
curl -sX POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"hotupdate.example.com.","type":"A","ttl":5,"value":"10.0.0.1"}' | jq .
dig @127.0.0.1 -p 5300 hotupdate.example.com. A +short
# 预期: 10.0.0.1

# 更新（保存返回的 ID）
ID=<上一步返回的 id>
curl -sX PUT http://localhost:8080/api/v1/domains/example.com./records/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"hotupdate.example.com.","type":"A","ttl":5,"value":"10.0.0.99"}'

# 立即查询，无需等待 TTL 过期
dig @127.0.0.1 -p 5300 hotupdate.example.com. A +short
# 预期: 10.0.0.99（不是 10.0.0.1）
```

---

## 7. 压力测试

### 7.1 工具安装

```bash
# dnsperf（推荐，Nominum/DNS-OARC 出品）
apt-get install -y dnsperf   # Debian/Ubuntu
# 或从源码编译: https://github.com/DNS-OARC/dnsperf

# 备选：flamethrower
go install github.com/DNS-OARC/flamethrower@latest
```

### 7.2 dnsperf 压测

```bash
# 准备查询文件
cat > /tmp/queries.txt <<'EOF'
www.example.com. A
api.example.com. A
example.com. MX
example.com. TXT
nonexistent.example.com. A
EOF

# 基础压测（30s，单线程）
dnsperf -s 127.0.0.1 -p 5300 -d /tmp/queries.txt -l 30

# 多线程并发
dnsperf -s 127.0.0.1 -p 5300 -d /tmp/queries.txt -l 30 -c 4 -Q 50000
```

**关注指标**

| 指标 | 建议目标 |
|------|---------|
| QPS（Queries/sec） | ≥ 50,000（单实例，4 核） |
| 平均延迟 | < 1 ms |
| P99 延迟 | < 5 ms |
| 丢包率 | 0% |

### 7.3 热更新并发压测

在 dnsperf 跑着的同时，并发发起 API 写请求，验证两者不互相干扰：

```bash
# 后台压测
dnsperf -s 127.0.0.1 -p 5300 -d /tmp/queries.txt -l 60 &
PERF_PID=$!

# 并发热更新（模拟高频更新）
for i in $(seq 1 100); do
  curl -s -X PUT http://localhost:8080/api/v1/domains/example.com./records/$ID \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"www.example.com.\",\"type\":\"A\",\"ttl\":5,\"value\":\"10.0.0.$((i % 256))\"}" &
done
wait

wait $PERF_PID
```

**预期**：dnsperf 最终报告无错误（Errors: 0），QPS 无明显下降。

---

## 8. 可观测性验证

### 8.1 指标完整性检查

```bash
# 执行若干查询后检查指标
for t in A AAAA MX TXT; do
  dig @127.0.0.1 -p 5300 example.com. $t > /dev/null
done
dig @127.0.0.1 -p 5300 nonexistent.example.com. A > /dev/null

curl -s http://localhost:8080/metrics | grep -E 'dns_queries_total|dns_query_duration'
```

**预期**：每种 `qtype` / `rcode` 组合均有对应计数，直方图桶有数据。

### 8.2 同步指标（需要 PG）

```bash
# 触发一次同步后检查
curl -s http://localhost:8080/metrics | grep dns_sync
```

**预期**

```
dns_sync_total{result="success"} 1
dns_sync_last_success_timestamp_seconds 1.7xxxxxxxxe+09
```

### 8.3 健康检查（K8s probe 模拟）

```bash
# 正常情况
curl -o /dev/null -sw '%{http_code}' http://localhost:8080/healthz
# 预期: 200

# 响应体
curl -s http://localhost:8080/healthz | jq .
# 预期: {"status":"ok"}
```

---

## 9. 已知测试限制

| 限制 | 说明 |
|------|------|
| 单元测试不覆盖 PG | `internal/pg` 包无单元测试，依赖集成测试验证 |
| 无自动化集成测试 | 目前集成测试为手动 dig/curl；可用 `bats` 或 shell 脚本自动化 |
| 分流比例验证为统计性 | 20 次查询的分布有较大随机波动，需 200+ 次才能稳定 |
| AXFR UDP 拒绝测试依赖 `dig` 行为 | 某些 `dig` 版本默认优先 TCP，需显式加 `+notcp` |
| 压测环境 | 结果受宿主机 CPU、网络栈、其他进程负载影响，仅供参考 |
