# 研究与发现

## 架构（已确认）

GoEdge 是 DNS 记录的 source of truth。dns-edge 是 GoEdge 管理的边缘 DNS 节点，通过 edgeDNSAPI 接收记录推送，纯内存提供 DNS 解析。

```
EdgeAdmin → edgeapi (GoEdge) → dns-edge edgeDNSAPI → ZoneStore → DNS 解析
```

**GoEdge 调用序列**（AddRecord 举例）：
1. `POST /NSDomainService/FindNSDomainWithName {"name":"example.com"}` → 获取 domainId
2. `POST /NSRecordService/CreateNSRecord {"nsDomainId":X,...}` → 创建记录

GoEdge 不调用 CreateNSDomain，因此 `FindNSDomainWithName` 需要 lazy-create zone。

## no-PG 模式运行路径

启动条件：Corefile 无 `postgres` 块，有 `api { listen ... edgedns_access_key_id ... }` 块

```
main.go:
  cfg.PG.DSN == ""  &&  cfg.API.Listen != ""
  → api.New(cfg.API, nil, zoneStore, log)  // pg=nil
  → registerEdgeDNSRoutes()  // 全部 14 个端点可用
```

`edgedns_provider.go` 中：
- Zone ID = FNV-1a hash of FQDN（稳定，重启后不变）
- Record ID = 进程内 `atomic.AddInt64(&recordIDCounter, 1)`（重启后从 1 开始）
- 所有读写直接操作 `s.store`（ZoneStore），零 PG 调用

## GoEdge edgeDNSAPI 客户端（edgeapi 侧）

文件：`/home/ivloli/Git_repo/edgeapi/internal/dnsclients/provider_edge_dns_api.go`

- `getToken()`：POST `/APIAccessTokenService/getAPIAccessToken` → Bearer token（24h TTL）
- 请求头：`X-Edge-Access-Token: <token>`
- 所有操作：先 `FindNSDomainWithName` 获取 domainId，再操作 Record

## 联调环境

- dns-edge：`-config Corefile.local`，监听 :5300（DNS）+ :8080（API）
- edgeapi：`/home/ivloli/Git_repo/edgeapi/build/`，GRPC :8031
- EdgeAdmin：`/home/ivloli/Git_repo/edgeadmin/build/edge-admin`，HTTP :7788
- MySQL：127.0.0.1:3306，database db_edge

## 配置文件位置

| 文件 | 说明 |
|------|------|
| `/home/ivloli/dns_dev/Corefile.local` | dns-edge 本机联调配置 |
| `/home/ivloli/Git_repo/edgeapi/configs/` + `build/configs/` | edgeapi 配置 |
| `/home/ivloli/Git_repo/edgeadmin/configs/` | edgeadmin 配置 |

edgeadmin nodeId/secret（连接 edgeapi 用）：
- nodeId: `22d93a9e9d2d73d2349dc630090379e8`
- secret: `pg05i8JWlhQ7PluJroTAjmla5fzo0Rvq`
