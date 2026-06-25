# dns-edge

一个基于 Go 实现的高性能权威 DNS 服务，支持通过 HTTP API 对 DNS 记录进行热更新，并具备基于权重的流量分流能力。

## 功能特性

- **标准权威 DNS**：支持 A / AAAA / CNAME / MX / TXT / NS / SOA 等常用记录类型
- **HTTP 热更新 API**：无需重启，通过 REST 接口实时增删改 DNS 记录
- **流量分流**：同一域名支持多个后端 IP，按权重比例返回不同解析结果
- **持久化存储**：DNS 记录存储于 PostgreSQL，重启后自动恢复
- **多实例部署**：支持水平扩展，实例间通过定时同步保持最终一致
- **DoT 支持**：通过前置 dnsdist 提供 DNS-over-TLS（端口 853）
- **AXFR Zone Transfer**：支持主从同步，供 slave 节点拉取完整 Zone 数据
- **容器化友好**：单二进制，可构建为极小 Docker 镜像

## 架构概览

```
客户端
  │
  ├── plain DNS (UDP/TCP :53)
  └── DNS-over-TLS (:853)
          │
          ▼
      dnsdist                    ← TLS 终止 / 负载均衡
          │  plain DNS (:5300)
          ▼
      dns-edge                   ← 本项目
      ├── DNS Handler            ← miekg/dns，处理查询
      ├── HTTP API (:8080)       ← Gin，热更新接口
      ├── ZoneStore              ← 内存，RWMutex 保护
      ├── PostgreSQL             ← 持久化，source of truth
      └── Nacos                  ← 分流权重（ListenConfig 推送）

      采样系统（独立）            ← 探测后端，写入 Nacos DataID 权重
```

## 技术栈

| 组件 | 技术 |
|------|------|
| DNS 核心 | [miekg/dns](https://github.com/miekg/dns) |
| HTTP API | [gin-gonic/gin](https://github.com/gin-gonic/gin) |
| 持久化 | PostgreSQL |
| 分流权重 | Nacos |
| DoT 前置 | [dnsdist](https://dnsdist.org) |
| 语言 | Go 1.25+ |

## 快速开始

### 依赖

- Go 1.25+
- PostgreSQL 14+
- Nacos 2.x（分流功能需要）
- dnsdist 1.9+（仅 DoT 需要）

### 运行

```bash
# 克隆项目
git clone <repo-url>
cd dns-edge

# 配置
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入 PG / Nacos 连接信息

# 启动
go run ./cmd/dns-edge
```

### Docker

```bash
docker build -t dns-edge .
docker run -p 5300:5300/udp -p 5300:5300/tcp -p 8080:8080 \
  -e PG_DSN="postgres://user:pass@host/db" \
  dns-edge
```

## HTTP API

所有接口前缀 `/api/v1`，返回 JSON。

### 域名记录管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/domains` | 列出所有域名 |
| `POST` | `/api/v1/domains` | 添加域名 |
| `DELETE` | `/api/v1/domains/:domain` | 删除域名及其所有记录 |
| `GET` | `/api/v1/domains/:domain/records` | 列出域名下所有记录 |
| `POST` | `/api/v1/domains/:domain/records` | 添加记录（立即生效） |
| `PUT` | `/api/v1/domains/:domain/records/:id` | 更新记录（立即生效） |
| `DELETE` | `/api/v1/domains/:domain/records/:id` | 删除记录（立即生效） |

### 示例：添加 A 记录

```bash
curl -X POST http://localhost:8080/api/v1/domains/example.com/records \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "www.example.com.",
    "type": "A",
    "ttl": 300,
    "value": "1.2.3.4"
  }'
```

### 示例：添加分流记录（多 IP 权重）

```bash
curl -X POST http://localhost:8080/api/v1/domains/example.com/records \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "api.example.com.",
    "type": "A",
    "ttl": 10,
    "backends": [
      {"value": "1.2.3.4", "weight": 70},
      {"value": "5.6.7.8", "weight": 30}
    ]
  }'
```

权重也可由采样系统写入 Nacos DataID（`dns_weights:api.example.com.:A`），DNS 服务通过 ListenConfig 回调自动热更新。

## 数据同步机制

DNS 记录写入流程（双写）：

```
HTTP API 收到变更请求
    │
    ├── 1. 写入 PostgreSQL（失败则返回错误，不继续）
    └── 2. 更新本实例内存 ZoneStore
```

其他实例的内存通过两种方式与 PG 同步：

- **定时轮询**：每 30 秒增量拉取（基于 `updated_at`）
- **概率触发**：每次 DNS 查询以 1% 概率触发增量同步

一致性特征：**最终一致**，窗口期约 0~30 秒，在 DNS TTL 容忍范围内。

## dnsdist 配置参考

```lua
-- /etc/dnsdist/dnsdist.conf
setACL({'0.0.0.0/0', '::/0'})

-- 标准 DNS（可选，允许明文查询）
addLocal('0.0.0.0:53', {doTCP=true})

-- DNS-over-TLS
addTLSLocal('0.0.0.0:853',
  '/etc/ssl/certs/dns.pem',
  '/etc/ssl/private/dns.key',
  {provider='openssl'})

-- 后端：本项目实例（可配多个实现负载均衡）
newServer({address='127.0.0.1:5300', checkInterval=5})
-- newServer({address='10.0.0.2:5300', checkInterval=5})
```

## 项目结构

```
dns-edge/
├── cmd/
│   └── dns-edge/        # 入口
├── internal/
│   ├── dns/             # DNS Handler，查询处理逻辑
│   ├── store/           # ZoneStore，内存存储层
│   ├── api/             # Gin HTTP API
│   ├── pg/              # PostgreSQL 持久化
│   ├── weight/          # WeightProvider（Nacos / Static / Composite）
│   └── sync/            # PG 增量同步调度器
├── config.example.yaml
├── Dockerfile
└── README.md
```

## 配置项

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `dns.listen` | `:5300` | DNS 监听地址 |
| `api.listen` | `:8080` | HTTP API 监听地址 |
| `pg.dsn` | — | PostgreSQL 连接字符串 |
| `nacos.addr` | — | Nacos 地址（分流权重，可选） |
| `sync.interval` | `30s` | PG 定时同步间隔 |
| `sync.prob` | `0.01` | 查询触发同步概率 |
