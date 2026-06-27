# 部署指南：GoEdge + dns-edge 完整联调环境

本文档描述从零开始在新机器上部署 edgeapi + edgeadmin + dns-edge 的完整流程，
包含编译、配置、启动和验证步骤。

## 环境要求

- OS：Linux x86_64（已在 Ubuntu 22.04 / Amazon Linux 2 验证）
- Go 1.21+（编译用，运行时不需要）
- MySQL 8.0+
- git

---

## 目录结构约定

```
/home/<user>/Git_repo/
├── edgeapi/          # GoEdge API 节点
├── edgeadmin/        # GoEdge 管理后台
├── edgecommon/       # 公共库（edgeadmin 依赖）
└── EdgeCommon -> edgecommon   # 软链接（edgeadmin go.mod 依赖）

/home/<user>/dns_dev/ # dns-edge 项目
```

---

## 一、MySQL 初始化

```bash
mysql -u root -p <<'SQL'
CREATE DATABASE IF NOT EXISTS db_edge CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
SQL
```

---

## 二、编译 edgeapi

```bash
cd /home/<user>/Git_repo/edgeapi
mkdir -p build
go build -o build/edge-api ./cmd/edge-api/
```

### 配置文件

配置放在 `configs/`（和 `build/` 同级，Tea.Root 会找到）：

**`configs/.db.yaml`**（edgeapi 实际读取的数据库配置）
```yaml
default:
  driver: mysql
  dsn: "root:你的密码@tcp(127.0.0.1:3306)/db_edge?charset=utf8mb4&parseTime=True"
```

**`configs/db.yaml`**（旧格式兼容，两个都建）
```yaml
host: 127.0.0.1:3306
database: db_edge
user: root
password: "你的密码"
prefix: edge
```

`configs/api.yaml` 首次启动后自动生成，**不需要手动创建**。

### 首次启动 edgeapi

```bash
cd /home/<user>/Git_repo/edgeapi
./build/edge-api
```

首次启动会自动建表并输出管理节点凭证，**记录以下信息**（edgeadmin 配置需要）：

```
[API_NODE]admin node id: 22d93a9e...
[API_NODE]admin node secret: pg05i8JW...
```

---

## 三、编译 edgeadmin

```bash
cd /home/<user>/Git_repo

# edgeadmin 的 go.mod 有 replace => ../EdgeCommon，需要软链接
ln -s edgecommon EdgeCommon   # 如果 EdgeCommon 不存在

cd edgeadmin
mkdir -p build
go build -o build/edge-admin ./cmd/edge-admin/
```

### 配置文件

**`configs/api_admin.yaml`**
```yaml
rpc.endpoints: [ "http://127.0.0.1:8031" ]
nodeId: "edgeapi 首次启动输出的 adminNodeId"
secret: "edgeapi 首次启动输出的 adminNodeSecret"
```

**`configs/server.yaml`**
```yaml
env: prod
http:
  "on": true
  listen: [ "0.0.0.0:7788" ]
https:
  "on": false
```

### 创建管理员账号

edgeapi 初始化后管理员表为空，需手动插入：

```bash
mysql -u root -p db_edge <<'SQL'
INSERT INTO edgeAdmins (username, password, fullName, isSuper, canLogin, state, createdAt)
VALUES ('admin', MD5('admin'), '管理员', 1, 1, 1, UNIX_TIMESTAMP());
SQL
```

> 生产环境将 `MD5('admin')` 换成 `MD5('强密码')`。

### 启动 edgeadmin

```bash
cd /home/<user>/Git_repo/edgeadmin
mkdir -p logs
./build/edge-admin >> logs/run.log 2>&1 &
```

访问 `http://<ip>:7788`，用 `admin` / `admin` 登录。

---

## 四、编译 dns-edge

```bash
cd /home/<user>/dns_dev
go build -o /usr/local/bin/dns-edge ./cmd/dns-edge/
```

### 配置文件

**`Corefile.local`**（无 PG 模式，GoEdge 通过 edgeDNSAPI 管理记录）

```
dns-edge {
    listen  :5300
    workers 0
    tcp     true

    api {
        listen :8080

        edgedns_access_key_id     your-key-id
        edgedns_access_key_secret your-key-secret
    }

    sync {
        interval  30s
        prob      0.01
        ratelimit 100
    }

    # 地理路由（可选）
    geo {
        xdb             /path/to/ip2region.xdb
        auto_update     true      # 自动从 GitHub 拉取最新 xdb
        update_interval 24h       # 检查间隔
        # github_token  ghp_xxx   # 可选，避免 API 限频
    }
}
```

> `edgedns_access_key_id` / `secret` 自定义，两边（Corefile 和 EdgeAdmin DNS 服务商配置）要一致。  
> `geo` 块可省略，省略后 DNS 解析不区分地域，所有请求返回全量记录（随机加权选择）。

### 启动 dns-edge

```bash
cd /home/<user>/dns_dev
nohup dns-edge -config Corefile.local >> /var/log/dns-edge.log 2>&1 &
```

---

## 五、在 GoEdge 配置 edgeDNSAPI Provider

### 5.1 添加 DNS 服务商

登录 EdgeAdmin → **DNS 管理** → **DNS 服务商** → 新建：

| 字段 | 值 |
|------|---|
| 名称 | 任意，如 `dns-edge-01` |
| 类型 | `EdgeDNS API` |
| Host | `http://<dns-edge-ip>:8080` |
| Access Key ID | 和 Corefile 里 `edgedns_access_key_id` 一致 |
| Access Key Secret | 和 Corefile 里 `edgedns_access_key_secret` 一致 |

### 5.2 添加 DNS 域名

进入刚建的服务商 → **新建域名**，填写要管理的域名（如 `example.com`）。

### 5.3 绑定 CDN 集群

EdgeAdmin → **节点管理** → **集群** → 选集群 → **DNS** 标签页：
- 选择 DNS 域名
- 填写二级域名前缀（如 `node`，节点 A 记录会是 `node.example.com`）

### 5.4 同步

在 DNS 域名页点「同步」，GoEdge 把集群节点 IP 推送到 dns-edge。

---

## 六、验证

```bash
# DNS 解析
dig @<dns-edge-ip> -p 5300 <子域名>.<域名> A +short

# API 健康检查
curl http://<dns-edge-ip>:8080/healthz
# → {"status":"ok"}
```

---

## 七、进程守护（systemd）

**`/etc/systemd/system/edge-api.service`**
```ini
[Unit]
Description=GoEdge API Node
After=network.target mysql.service

[Service]
Type=simple
User=<user>
WorkingDirectory=/home/<user>/Git_repo/edgeapi/build
ExecStart=/home/<user>/Git_repo/edgeapi/build/edge-api
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/edge-admin.service`**
```ini
[Unit]
Description=GoEdge Admin
After=edge-api.service

[Service]
Type=simple
User=<user>
WorkingDirectory=/home/<user>/Git_repo/edgeadmin
ExecStart=/home/<user>/Git_repo/edgeadmin/build/edge-admin
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/dns-edge.service`**
```ini
[Unit]
Description=dns-edge
After=network.target

[Service]
Type=simple
User=<user>
WorkingDirectory=/home/<user>/dns_dev
ExecStart=/usr/local/bin/dns-edge -config Corefile.local
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now edge-api edge-admin dns-edge
```

---

## 八、常见问题

**edgeadmin 启动找不到配置**

`Tea.Root` 由二进制路径决定：二进制在 `build/` 子目录，`configs/` 和 `build/` 同级才能被找到。

**edgeadmin 编译失败（找不到 EdgeCommon）**

```bash
ls /home/<user>/Git_repo/EdgeCommon  # 确认软链接存在
ln -s edgecommon /home/<user>/Git_repo/EdgeCommon  # 不存在时创建
```

**dns-edge 重启后记录丢失**

无需手动操作。edgeapi 的 `DNSTaskExecutor` 每 20 秒检测一次 dns-edge 的 domain 列表，发现为空时自动触发重推。实测重启后 **5 秒到 20 秒内**记录自动恢复。

如果等待超过 1 分钟仍未恢复，可手动在 EdgeAdmin DNS 域名页点「同步」强制触发。

**dig 返回空/NXDOMAIN**

先确认 dns-edge 里有记录，再触发 EdgeAdmin 同步：
```bash
curl http://<dns-edge-ip>:8080/healthz
# 然后 EdgeAdmin → DNS 服务商 → 域名 → 同步
```
