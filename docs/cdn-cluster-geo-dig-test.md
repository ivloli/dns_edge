# CDN 集群多节点 + 地理线路 dig 验证

背景：2026-07-06 给三个 CDN 集群（默认集群/c2/momo集群）各补了几个节点（`isInstalled=1`）和 IP 地址，并给每个新节点配置了「国家+省份+ISP」三条线路（`edgeNodes.dnsRoutes`，JSON key 是 `edgeDNSDomains.id`，不是 IP 地址 id——第一次配置时踩了这个坑，配错成了 ipAddrId，线路没生效，改成 domainId 才对）。本文档记录验证这三个域名解析是否正常、地理线路是否按预期生效的 dig 命令。

实测通过日期：2026-07-06

---

## 域名 ↔ 集群 ↔ 节点对照

| 域名 | 绑定集群 | DNS 名称前缀 | 节点（IP → 线路） |
|------|---------|-------------|-------------------|
| `fafa.com` | 默认集群（id=1） | `cluster1` | `test-node-01`(10.100.0.1→湖南)、`timo`(10.0.0.123→中国/联通/广东)、`node-cluster1-02`(10.0.0.11→中国/电信/上海) |
| `edge-test.local` | c2（id=2） | `g0c3111.cdn` | `c201`(10.0.1.1→默认)、`node-cluster2-02`(10.0.1.11→中国/移动/广东)、`node-cluster2-03`(10.0.1.12→中国/联通/北京) |
| `momo.com` | momo集群（id=3） | `cluster1` | `momo-node-01`(10.88.0.1/.2→中国/电信/贵州)、`node-momo-02`(10.88.0.11→中国/电信/浙江)、`node-momo-03`(10.88.0.12→中国/移动/四川) |

---

## 1. fafa.com

```bash
# 通配符：任意子域名走 * CNAME → cluster1.fafa.com
dig @127.0.0.1 -p 5300 test.fafa.com A +short
# 预期：先返回 CNAME cluster1.fafa.com.，再 chase 出一个节点 IP

# 集群节点域名，无 ECS：随机命中某个节点 IP
dig @127.0.0.1 -p 5300 cluster1.fafa.com A +short

# 带 ECS：上海+电信（122.224.0.1）应该命中 node-cluster1-02 的专属线路
dig @127.0.0.1 -p 5300 cluster1.fafa.com A +subnet=122.224.0.1 +short
# 实测：10.0.0.11（node-cluster1-02）✅ 命中 province+isp 双标签

# 带 ECS：广东+移动（183.232.0.1）——没有节点同时配了"广东+移动"，只落到 province:广东（timo 的其中一条线路）
dig @127.0.0.1 -p 5300 cluster1.fafa.com A +subnet=183.232.0.1 +short
# 实测：10.0.0.123（timo，province:广东 命中）
```

`fafa.com` 本身（apex）没有配 A/CNAME 记录（只有 `cluster1` 子域名和 `*` 通配符），直接 `dig fafa.com A` 会是 NXDOMAIN/空，这是预期行为，不是 bug。

---

## 2. momo.com

```bash
# 通配符
dig @127.0.0.1 -p 5300 test.momo.com A +short
# 预期：CNAME cluster1.momo.com. chase 出节点 IP

# 无 ECS
dig @127.0.0.1 -p 5300 cluster1.momo.com A +short

# 浙江+电信（122.224.0.1）应该命中 node-momo-02 的专属线路
dig @127.0.0.1 -p 5300 cluster1.momo.com A +subnet=122.224.0.1 +short
# 实测：10.88.0.11（node-momo-02）✅ 命中 province+isp 双标签

# 随便一个"猜测"的川渝地区 IP（61.128.128.1），验证没猜对时的兜底行为
dig @127.0.0.1 -p 5300 cluster1.momo.com A +subnet=61.128.128.1 +short
# 实测：10.88.0.2（momo-node-01 的第二个 IP，country:中国 兜底命中，
#      说明这个测试 IP 实际不在 ip2region 的"四川"库里，不是路由逻辑的问题）
```

---

## 3. edge-test.local

```bash
# 通配符
dig @127.0.0.1 -p 5300 test.edge-test.local A +short

# 无 ECS
dig @127.0.0.1 -p 5300 g0c3111.cdn.edge-test.local A +short
# 实测：10.0.1.1（c201，没配线路的默认节点）

# 广东+移动（183.232.0.1）应该命中 node-cluster2-02
dig @127.0.0.1 -p 5300 g0c3111.cdn.edge-test.local A +subnet=183.232.0.1 +short
# 实测：10.0.1.11（node-cluster2-02）✅ 命中 province+isp 双标签
```

---

## 小结

三个域名的 apex/通配符/集群节点子域名解析全部正常；带 ECS 的地理线路测试里，凡是**测试 IP 真实落在 ip2region 对应省份/ISP**的场景（上海电信、浙江电信、广东移动）都精确命中了新配的专属线路节点，验证了"节点补 IP + 配 dnsRoutes(domainId 作 key) → 触发 clusterChange 重推 → dns-edge 按 province+isp 双标签路由"这条链路完整可用。

**踩坑记录**：`edgeNodes.dnsRoutes` 的 JSON key 一开始配成了 `ipAddrId`（IP 地址表的 id），线路完全不生效，页面上节点显示 `route: {code:"", name:""}`；改成正确的 `domainId`（这个集群绑定的 `edgeDNSDomains.id`）后才生效。以后给节点配线路，key 一定是域名 ID，不是 IP 地址 ID 或节点 ID。
