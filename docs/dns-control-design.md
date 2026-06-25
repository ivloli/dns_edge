# dns-control 中心控制服务方案

**版本**：v1.0  
**日期**：2026-06-25

---

## 1. 背景与问题

当前 dns-edge 每个边缘节点直接连接中心 PostgreSQL 和 Nacos：

```
边缘节点 A（东京） ──── 直连 ────► 中心 PG（北京）
边缘节点 B（法兰克福） ── 直连 ──► 中心 PG（北京）
边缘节点 C（圣保罗） ─── 直连 ──► 中心 Nacos（北京）
...（N 个节点）
```

存在以下问题：

1. **安全边界宽**：PG/Nacos 凭证下发到所有边缘节点，一个节点被入侵即可横向访问数据库
2. **网络延迟高**：跨洲直连 PG 做增量同步，TCP 延迟 100-300ms，同步窗口受限
3. **连接数压力**：N 个边缘节点同时持有 PG 连接池，连接数随节点数线性增长
4. **Nacos 暴露面广**：Nacos 管理端口暴露给全球节点，攻击面大

---

## 2. 方案：dns-control 中心控制服务

在中心部署一个轻量 HTTP 服务 `dns-control`，作为边缘节点唯一的数据入口。边缘节点不再直连 PG 或 Nacos。

```
┌─────────────────────────────────────────────────────────┐
│  中心（北京/主可用区）                                    │
│                                                         │
│  [dns-control]  ─── 内网直连 ───► PostgreSQL             │
│       │         ─── 内网直连 ───► Nacos                  │
│       │                                                 │
│  [dns-control-standby]（热备）                           │
└──────────────────┬──────────────────────────────────────┘
                   │ HTTPS（公网或专线）
        ┌──────────┴──────────┐
        ▼                     ▼
  边缘节点 A（东京）     边缘节点 B（法兰克福）
  [dns-edge]             [dns-edge]
  只知道 dns-control     只知道 dns-control
  的地址和 API Token     的地址和 API Token
```

---

## 3. dns-control 接口设计

### 3.1 鉴权

所有接口使用 Bearer Token，token 在 Corefile 中配置，定期轮换。

```
Authorization: Bearer <token>
```

### 3.2 接口列表

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/v1/sync/incremental` | 增量拉取变更记录 |
| `GET` | `/v1/sync/full` | 全量拉取（节点首次启动时） |
| `GET` | `/v1/weights/stream` | SSE 长连接，推送权重变更 |
| `GET` | `/v1/weights/{domain}` | 查询指定域名当前权重（短连接降级） |
| `GET` | `/healthz` | 健康检查 |

### 3.3 增量同步接口

**请求**
```
GET /v1/sync/incremental?since=<unix_ms>
```

**响应**
```json
{
  "until": 1719302400000,
  "zones": [
    { "id": 1, "name": "example.com.", "deleted": false }
  ],
  "records": [
    { "id": 42, "zone_id": 1, "name": "www.example.com.", "type": "A",
      "ttl": 300, "value": "1.2.3.4", "weight": 10, "deleted": false }
  ]
}
```

`deleted: true` 表示软删除，边缘节点收到后从 ZoneStore 移除对应记录。`until` 作为下次请求的 `since` 参数。

### 3.4 权重推送接口（SSE）

```
GET /v1/weights/stream
```

服务端以 Server-Sent Events 格式推送，每条事件格式：

```
event: weight_update
data: {"domain":"www.example.com.","qtype":1,"weights":{"1.2.3.4":30,"5.6.7.8":70}}

event: ping
data: {}
```

边缘节点断线后自动重连，重连时先调一次 `/v1/weights/{domain}` 补齐丢失的推送。

---

## 4. dns-edge 侧改动

### 4.1 配置变化

移除 `postgres` 和 `nacos` 块，新增 `control` 块：

```
dns-edge {
    listen  :15300
    api {
        listen  :28082
        goedge_secret  <secret>
    }
    control {
        url    https://dns-control.internal
        token  <bearer-token>
    }
    sync {
        interval  30s
        prob      0.01
        ratelimit 100
    }
}
```

### 4.2 模块改动

| 模块 | 现在 | 改后 |
|------|------|------|
| `internal/pg/` | 直连 PG，读写记录 | **保留**（dns-control 侧使用） |
| `internal/weight/nacos.go` | 直连 Nacos | 替换为 SSE 客户端 |
| `internal/syncer/` | 调 `pg.Store.IncrementalLoad` | 改调 `control.Client.IncrementalLoad` |
| **新增** `internal/control/` | — | HTTP 客户端，封装两个接口 |

dns-edge 自身不再需要 PG 驱动和 Nacos SDK，二进制体积缩小。

---

## 5. dns-control 服务实现

独立 Go 服务，目录结构建议：

```
cmd/dns-control/
internal/
  controlapi/   HTTP handlers
  pg/           复用 dns-edge 的 pg 包
  nacos/        复用 dns-edge 的 weight/nacos.go
```

关键点：
- **SSE 连接管理**：维护一个 `sync.Map[connID → chan Event]`，Nacos 推送回调时 fan-out 给所有在线连接
- **增量查询**：直接复用现有 `pg.Store.IncrementalLoad` 逻辑，将结果序列化为 JSON 返回
- **高可用**：两实例 Active-Active，前置 LB（Nginx/HAProxy）。无状态，SSE 断线重连天然幂等

---

## 6. 开发阶段规划

| 阶段 | 内容 | 预估 |
|------|------|------|
| P1 | `cmd/dns-control/` 骨架 + `/v1/sync/incremental` 接口 | 1 天 |
| P2 | SSE 权重推送 + Nacos 回调 fan-out | 1 天 |
| P3 | `internal/control/` 客户端（dns-edge 侧替换 pg/nacos 直连） | 1.5 天 |
| P4 | 配置层改造（移除 `postgres`/`nacos` 块，新增 `control` 块） | 0.5 天 |
| P5 | 单元测试 + 集成测试 | 1 天 |
| P6 | 部署文档（docker-compose / K8s） | 0.5 天 |

总计约 **5.5 天**。

---

## 7. 待确认事项

1. **dns-control 部署位置**：单独一个中心机房，还是跟着 PG 同机？
2. **鉴权方式**：Bearer Token 是否足够，还是需要 mTLS？
3. **dns-edge 写路径**：GoEdge 调 `AddRecord` 触发的写，还是走 dns-control 代理，还是 dns-edge 直接写 PG？（推荐后者保持简单，dns-control 只管读/推）
4. **全量同步触发时机**：节点首次启动时调 `/v1/sync/full`，之后纯增量。是否需要定期全量兜底？
