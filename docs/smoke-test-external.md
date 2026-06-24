# dns-edge 外部冒烟测试（通过 Nginx 代理）

在任意外部机器上执行，通过 Nginx 专线代理访问测试环境。

---

## 0. 环境变量（执行测试前先设置）

```bash
# Nginx 代理机 IP（专线入口）
export NGINX_IP=120.25.74.229

# 对外暴露的端口（Nginx 侧）
export DNS_PORT=15300      # DNS 查询端口（UDP + TCP）
export API_PORT=28082      # HTTP API / Prometheus 端口

# 快捷变量
export DNS="@${NGINX_IP} -p ${DNS_PORT}"
export API="http://${NGINX_IP}:${API_PORT}"
```

---

## 1. 启动检查

```bash
curl -s ${API}/healthz
```

**预期**：`{"status":"ok"}`，HTTP 200

---

## 2. 通过 API 创建测试数据

```bash
# 建域名
curl -sX POST ${API}/api/v1/domains \
  -H 'Content-Type: application/json' \
  -d '{"name":"example.com."}' | jq .
# 预期: {"id":1,"name":"example.com."}

# A 记录（单条）
curl -sX POST ${API}/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"www.example.com.","type":"A","ttl":300,"value":"1.2.3.4"}' | jq .

# A 记录（两条，带权重，用于分流验证）
curl -sX POST ${API}/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.1","weight":70}' | jq .
curl -sX POST ${API}/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"10.0.0.2","weight":30}' | jq .
```

---

## 3. DNS 功能验证

### 3.1 A 记录查询

```bash
dig ${DNS} www.example.com. A +short
```

**预期**：`1.2.3.4`

### 3.2 加权分流（StaticWeightProvider）

```bash
for i in $(seq 1 20); do dig ${DNS} api.example.com. A +short; done | sort | uniq -c
```

**预期**：`10.0.0.1` 约 14 次，`10.0.0.2` 约 6 次（70/30，有统计波动）

### 3.3 NXDOMAIN（名称不存在）

```bash
dig ${DNS} notexist.example.com. A +noall +comments | grep status
```

**预期**：`status: NXDOMAIN`

### 3.4 NODATA（名称存在但类型不匹配）

```bash
dig ${DNS} www.example.com. MX +noall +comments | grep status
```

**预期**：`status: NOERROR`（ANSWER 为空，AUTHORITY 有 SOA）

### 3.5 REFUSED（不在授权范围）

```bash
dig ${DNS} google.com. A +noall +comments | grep status
```

**预期**：`status: REFUSED`

### 3.6 RFC 8482 TypeANY

```bash
dig ${DNS} www.example.com. ANY +short
```

**预期**：`"RFC8482" ""`

### 3.7 EDNS0 协商

```bash
dig ${DNS} www.example.com. A +edns=0 | grep "udp:"
```

**预期**：`udp: 4096`

### 3.8 ECS 回显（带子网）

```bash
dig ${DNS} www.example.com. A +subnet=1.2.3.0/24
```

**预期**：OPT PSEUDOSECTION 含 `CLIENT-SUBNET: 1.2.3.0/24/0`（source=24，scope=0）

### 3.9 ECS 回显（不带子网）

```bash
dig ${DNS} www.example.com. A +edns=0 +nosubnet | grep -A5 "OPT PSEUDOSECTION"
```

**预期**：OPT 只有 `udp: 4096`，无 `CLIENT-SUBNET` 行

### 3.10 AXFR Zone Transfer（TCP）

```bash
dig ${DNS} example.com. AXFR +tcp | grep -v "^;"
```

**预期**：SOA → A 记录 → SOA，包含所有域名记录

### 3.11 TCP 查询

```bash
dig ${DNS} www.example.com. A +tcp +short
```

**预期**：`1.2.3.4`

---

## 4. 热更新验证

```bash
# 添加记录，保存 ID
ID=$(curl -sX POST ${API}/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"hottest.example.com.","type":"A","ttl":5,"value":"10.0.1.1"}' | jq -r .id)

# 查初始值
dig ${DNS} hottest.example.com. A +short
# 预期: 10.0.1.1

# 热更新
curl -sX PUT ${API}/api/v1/domains/example.com./records/${ID} \
  -H 'Content-Type: application/json' \
  -d '{"name":"hottest.example.com.","type":"A","ttl":5,"value":"10.0.1.99"}' | jq .

# 立即查询（无需等 TTL 过期）
dig ${DNS} hottest.example.com. A +short
# 预期: 10.0.1.99（变更立即生效）
```

---

## 5. Prometheus 指标

```bash
curl -s ${API}/metrics | grep -E '^dns_'
```

**预期关键指标**：

| 指标 | 说明 |
|------|------|
| `dns_queries_total{qtype="A",rcode="NOERROR"}` | A 查询成功计数 |
| `dns_queries_total{qtype="A",rcode="NXDOMAIN"}` | NXDOMAIN 计数 |
| `dns_queries_total{qtype="A",rcode="REFUSED"}` | REFUSED 计数 |
| `dns_query_duration_seconds_bucket` | 延迟分布直方图 |
| `dns_sync_total{result="success"}` | 增量同步成功次数 |

---

## 6. 冒烟测试检查单

| 项目 | 命令 | 预期结果 | 实测 |
|------|------|----------|------|
| 进程存活 | `curl ${API}/healthz` | `{"status":"ok"}` | |
| A 记录 | `dig ${DNS} www.example.com. A +short` | `1.2.3.4` | |
| 加权分流 | 20 次 dig api | ~14/6（70/30）| |
| NXDOMAIN | `dig ${DNS} notexist... A` | `status: NXDOMAIN` | |
| NODATA | `dig ${DNS} www... MX` | `status: NOERROR`，ANSWER 空 | |
| REFUSED | `dig ${DNS} google.com.` | `status: REFUSED` | |
| RFC 8482 ANY | `dig ${DNS} www... ANY` | `"RFC8482" ""` | |
| EDNS0 | `dig ${DNS} +edns=0` | OPT `udp: 4096` | |
| ECS 回显（带子网）| `dig ${DNS} +subnet=1.2.3.0/24` | OPT 含 `CLIENT-SUBNET: 1.2.3.0/24/0` | |
| ECS 回显（无子网）| `dig ${DNS} +edns=0 +nosubnet` | OPT 无 `CLIENT-SUBNET` 行 | |
| AXFR TCP | `dig ${DNS} example.com. AXFR +tcp` | SOA→记录→SOA | |
| 热更新 | PUT 记录后立即 dig | 新值立即生效 | |
| Prometheus | `curl ${API}/metrics` | dns_* 指标均有数据 | |
