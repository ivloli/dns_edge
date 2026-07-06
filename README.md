# dns-edge

一个基于 Go 实现的高性能权威 DNS 服务，纯内存存储，支持通过 HTTP API 热更新 DNS 记录、基于权重的流量分流，以及基于 ip2region 的地理路由（ECS 感知）。

## 功能特性

- **标准权威 DNS**：支持 A / AAAA / CNAME / MX / TXT / NS / SOA 等常用记录类型
- **HTTP 热更新 API**：无需重启，通过 REST 接口实时增删改 DNS 记录
- **流量分流**：同一域名支持多个后端 IP，按权重比例返回不同解析结果
- **地理路由**：基于 EDNS Client Subnet + ip2region xdb，按省份 / 运营商 / 国家分流
- **GoEdge 集成**：兼容 GoEdge customHTTP Provider 和 edgeDNSAPI 双接口，支持智能 DNS 分流配置
- **纯内存存储**：ZoneStore 是唯一存储后端，无数据库依赖；由 GoEdge EdgeAPI 推送/轮询同步
- **多实例部署**：支持水平扩展，节点间通过 edgeapi 推送 + 20s 轮询兜底保持最终一致
- **DoT 支持**：通过前置 dnsdist 提供 DNS-over-TLS（端口 853）
- **AXFR Zone Transfer**：支持主从同步，供 slave 节点拉取完整 Zone 数据
- **容器化友好**：单二进制，可构建为极小 Docker 镜像

## 架构概览

```
GoEdge EdgeAdmin
    │
    ▼
GoEdge EdgeAPI（edgeapi，MySQL）
    │
    ├─ 推送（变更即通知）→ dns-edge /internal/sync
    └─ 轮询（兜底）    ← dns-edge 定期拉取

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
        ├── HTTP API (:8080)       ← Gin，热更新 / GoEdge 接口
        ├── ZoneStore              ← 纯内存，RWMutex + COW 保护
        ├── WeightProvider         ← Nacos 动态权重 + 地理路由
        └── ip2region xdb          ← ECS 地理分流，VectorIndex 模式
```

## 技术栈

| 组件 | 技术 |
|------|------|
| DNS 核心 | [miekg/dns](https://github.com/miekg/dns) |
| HTTP API | [gin-gonic/gin](https://github.com/gin-gonic/gin) |
| 存储 | 纯内存 ZoneStore（无数据库） |
| 同步 | GoEdge edgeDNSAPI 推送 + 轮询 |
| 分流权重 | Nacos |
| 地理路由 | [ip2region](https://github.com/lionsoul2014/ip2region) xdb |
| DoT 前置 | [dnsdist](https://dnsdist.org) |
| 语言 | Go 1.25+ |

## 快速开始

### 依赖

- Go 1.25+
- Nacos 2.x（分流权重，可选）
- ip2region.xdb（地理路由，可选）
- GoEdge EdgeAPI（记录同步，生产环境）
- dnsdist 1.9+（仅 DoT 需要）

### 运行

```bash
# 克隆项目
git clone <repo-url>
cd dns-edge

# 编写 Corefile
cat > Corefile <<'EOF'
dns-edge {
    dns {
        listen :5300
        tcp    true
    }
    api {
        listen                    :8080
        edgedns_access_key_id     <your-key-id>
        edgedns_access_key_secret <your-key-secret>
        goedge_secret             <your-goedge-secret>
    }
    nacos {
        addr       127.0.0.1:8848
        namespace  default
    }
    geo {
        xdb  /etc/dns-edge/ip2region.xdb
    }
}
EOF

# 启动
./dns-edge -config Corefile
```

### Docker

```bash
docker build -t dns-edge .
docker run -p 5300:5300/udp -p 5300:5300/tcp -p 8080:8080 \
  -e EDGEDNS_ACCESS_KEY_ID="your-key-id" \
  -e EDGEDNS_ACCESS_KEY_SECRET="your-key-secret" \
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

### GoEdge 接口

| 类型 | 端点 | 说明 |
|------|------|------|
| customHTTP | `POST /goedge/dns` | GoEdge customHTTP Provider |
| edgeDNSAPI | `POST /NS*Service/*` | GoEdge 原生 EdgeDNS 接口（含地理路由线路） |
| 健康检查 | `GET /healthz` | `{"status":"ok","zoneCount":N}` |

### 示例：添加 A 记录

```bash
curl -X POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"www.example.com.","type":"A","ttl":300,"value":"1.2.3.4"}'
```

### 示例：添加带地理路由的记录

```bash
# 上海电信专线
curl -X POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"1.2.3.4","route_tags":"province=上海;isp=电信"}'

# 默认兜底
curl -X POST http://localhost:8080/api/v1/domains/example.com./records \
  -H 'Content-Type: application/json' \
  -d '{"name":"api.example.com.","type":"A","ttl":10,"value":"5.6.7.8"}'
```

## 数据同步机制

dns-edge 节点不直连任何数据库，所有记录通过 GoEdge EdgeAPI 同步：

```
GoEdge EdgeAPI（中心，MySQL）
    │
    ├─ 推送（低延迟）：变更时主动通知 dns-edge，dns-edge 写内存
    └─ 轮询（兜底）：dns-edge 每 20s 检查 zoneCount，为 0 时触发全量拉取
```

一致性特征：**最终一致**，推送路径延迟 < 1s，轮询兜底窗口 20s。

## 地理路由

基于 EDNS Client Subnet（RFC 7871）和 ip2region xdb 实现按地理位置分流：

- 优先级：`省份+运营商 > 省份 > 运营商 > 国家 > 默认 > 全量`
- xdb 支持自动更新（GitHub Releases 定期拉取，atomic 热替换）
- `geo {}` 块缺失或 xdb 不存在时自动退化为纯权重模式

## dnsdist 配置参考

```lua
setACL({'0.0.0.0/0', '::/0'})
addLocal('0.0.0.0:53', {doTCP=true})
addTLSLocal('0.0.0.0:853',
  '/etc/ssl/certs/dns.pem',
  '/etc/ssl/private/dns.key',
  {provider='openssl'})
newServer({address='127.0.0.1:5300', checkInterval=5})
```

## 项目结构

```
dns-edge/
├── cmd/
│   └── dns-edge/        # 入口
├── internal/
│   ├── dns/             # DNS Handler，查询处理逻辑
│   ├── store/           # ZoneStore，纯内存存储层
│   ├── api/             # Gin HTTP API（含 GoEdge 接口）
│   ├── geo/             # ip2region xdb 封装 + filterByGeo
│   ├── weight/          # WeightProvider（Nacos / Static / Composite）
│   └── iface/           # 接口定义（ZoneStore、WeightProvider）
├── config/              # Corefile 解析
├── Dockerfile
└── README.md
```

## 配置项（Corefile）

| 块 | 字段 | 说明 |
|----|------|------|
| `dns {}` | `listen` | DNS 监听地址，默认 `:5300` |
| `dns {}` | `tcp` | 启用 TCP，默认 `true` |
| `api {}` | `listen` | HTTP API 监听地址，默认 `:8080` |
| `api {}` | `edgedns_access_key_id` | edgeDNSAPI 鉴权 Key ID |
| `api {}` | `edgedns_access_key_secret` | edgeDNSAPI 鉴权 Key Secret |
| `api {}` | `goedge_secret` | customHTTP Provider 共享密钥 |
| `nacos {}` | `addr` | Nacos 地址（分流权重，可选） |
| `geo {}` | `xdb` | ip2region xdb 文件路径（地理路由，可选） |
