# 部署指南：GoEdge + dns-edge 完整联调环境

本文档描述部署 edgeapi + edgeadmin + dns-edge（含 NS/智能DNS 模块）的完整流程。
2026-07-06 起四个仓库（edgecommon/edgeapi/edgeadmin/dns-edge）统一迁移到
`feature/ns-dns-edge` 分支，本文档按这个分支的实际状态重写。

## 环境要求

- OS：Linux x86_64（已在 Ubuntu 22.04 / Amazon Linux 2 验证）
- MySQL 8.0+
- `ip2region.xdb`（geo 路由用，可选功能，见下文）
- **两种部署方式，视目标机器而定**：
  - **本机/开发机**：需要 Go 1.21+ 工具链 + 对 `gitlab.gainetics.io` 私有仓库的 SSH/HTTPS 访问权限（edgeapi/edgeadmin 依赖公司私有 `edgecommon` fork，走本地 `replace` 路径依赖，不走公共 Go module proxy）
  - **全新目标机器（无需上面这些）**：直接用已经编译好的 tarball 包，见「方式二」

---

## 目录结构约定（本机编译时必须遵守）

```
/home/<user>/Git_repo/
├── edgecommon/       # 公司私有 EdgeCommon fork（edgeapi/edgeadmin 依赖，走本地 replace）
├── edgeapi/          # GoEdge API 节点
└── edgeadmin/        # GoEdge 管理后台

/home/<user>/dns_dev/ # dns-edge 项目（独立仓库，不依赖 edgecommon）
```

`edgecommon` 必须和 `edgeapi`/`edgeadmin` 平级（`../edgecommon` 相对路径），因为
两者的 `go.mod` 里都有：

```go
replace gitlab.gainetics.io/backend-cdn/goedge/edgecommon => ../edgecommon
```

**不要**把这个依赖换成 `github.com/TeaOSLab/EdgeCommon`（公共 proxy.golang.org
能下载到的开源公版）——那是同名但内容不同的另一个项目，缺公司私有定制字段，
换了会悄悄丢功能且编译期不一定报错，详见文末「常见问题」。

四个仓库都要切到同一个分支：

```bash
cd /home/<user>/Git_repo/edgecommon  && git checkout feature/ns-dns-edge
cd /home/<user>/Git_repo/edgeapi     && git checkout feature/ns-dns-edge
cd /home/<user>/Git_repo/edgeadmin   && git checkout feature/ns-dns-edge
cd /home/<user>/dns_dev              && git checkout feature/ns-dns-edge
```

---

## 一、MySQL 初始化

```bash
mysql -u root -p <<'SQL'
CREATE DATABASE IF NOT EXISTS db_edge CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
SQL
```

edgeapi 首次启动会自动建表（含 NS 相关的 `edgeNS*` 表），不需要手动建表。

---

## 方式一：本机一键部署（推荐用于开发/联调机）

四个仓库都按上面「目录结构约定」clone 好、切到 `feature/ns-dns-edge` 后：

```bash
cd /home/<user>/dns_dev
./scripts/deploy-test-env.sh
```

这个脚本会依次做：

1. 检查 Go 工具链、MySQL 连通性（默认 `127.0.0.1:3306` `root`/`123456` `db_edge`，
   可用环境变量 `MYSQL_HOST`/`MYSQL_PORT`/`MYSQL_USER`/`MYSQL_PASSWORD`/`MYSQL_DATABASE`
   覆盖）、`ip2region.xdb` 文件是否存在（默认路径
   `/home/ivloli/edge/static/ip2region.xdb`，可用 `IP2REGION_XDB` 覆盖）
2. 确认四个仓库都在 `feature/ns-dns-edge` 分支（不对会直接报错退出，不会替你切分支）
3. 按依赖顺序 `edgeapi → edgeadmin → dns-edge` 依次执行各仓库自己的
   `make deploy-local`（见下方「本地进程管理」）
4. 跑一遍烟测：`healthz`、`dig test.local A`、edgeadmin 首页可达

首次运行前，三个服务各自的配置文件需要手动准备好（因为含真实凭证，从不入库，见
下面「配置文件」一节）；配置文件一旦放好，之后重复运行这个脚本就是纯粹的"重新
编译+重启+验证"，不需要再碰配置。

### 本地进程管理（`make` 系列命令）

`edgeapi`、`edgeadmin`、`dns-edge` 三个仓库都有一份 Makefile，提供一套不需要
`sudo`/`systemd` 的本地进程管理（这台机器如果没有免密 sudo，systemd 那套装不
上，才需要这套）：

| 命令 | 作用 |
|------|------|
| `make build` | 编译（edgeadmin 是 `CGO_ENABLED=0 go build`） |
| `make run-local` | 编译并启动（已在运行则跳过），PID 写到 `.run/<name>.pid` |
| `make stop-local` | 按 PID 文件停止 |
| `make restart-local` | stop + run |
| `make deploy-local` | restart + 健康检查（edgeapi 查 gRPC 端口、edgeadmin/dns-edge 查 HTTP） |
| `make status-local` | 查进程是否存活 |

单独管理某个服务时直接在对应仓库目录下跑这些命令即可，不需要走整个
`deploy-test-env.sh`。

---

## 方式二：打包部署到全新机器

全新机器不需要 Go 工具链、不需要私有仓库访问权限——因为编译这一步已经在
「方式一」的开发机上完成，产出的是纯二进制。

**在已经配置好依赖的机器上打包：**

```bash
cd /home/<user>/dns_dev
./scripts/package-release.sh
```

产出一个 `release-artifacts/<时间戳>/` 目录，包含三个 tarball（
`edge-api-test-env.tar.gz`、`edge-admin-test-env.tar.gz`、
`dns-edge-linux-amd64-<tag>.tar.gz`）和一份 `README.txt`。把这个目录整体
`scp`/`rsync` 到目标机器。

**在目标机器上（对每个 tarball 重复）：**

```bash
mkdir -p /opt/<service> && tar -xzf <name>.tar.gz -C /opt/<service>
cd /opt/<service>/configs
cp X.template.yaml X.yaml   # 每个 .template.yaml 都要复制成同名去掉 template 的文件
vim X.yaml                  # 填真实的数据库密码/nodeId/secret等（模板里只有占位符）
cd ..
nohup ./<binary> > run.log 2>&1 &
```

启动顺序：**edgeapi 先起**（edgeadmin 和 dns-edge 的 edgeagent 都要连它的 gRPC
:8031），然后 edgeadmin、dns-edge 顺序不限。MySQL 和 `ip2region.xdb`
不在打包范围内，目标机器要自己准备。

---

## 二、配置文件

无论走哪种部署方式，三个服务各自的配置文件都不入库（`.gitignore` 排除），
需要手动准备。**模板文件**（`*.template.yaml`，占位符，可以放心参考）：

- edgeapi: `<edgeapi>/build/configs/api.template.yaml`、`db.template.yaml`
- edgeadmin: `<edgeadmin>/build/configs/api_admin.template.yaml`、
  `api_db.template.yaml`、`server.template.yaml`

### edgeapi

**`configs/db.yaml`**（和 `build/` 同级——见下面 Tea.Root 说明）：
```yaml
host: 127.0.0.1:3306
database: db_edge
user: root
password: "你的密码"
```

`configs/api.yaml` 首次启动后自动生成 `nodeId`/`secret`，**不需要手动创建**；
记下自动生成的这两个值，edgeadmin 那边要用。

### edgeadmin

**`configs/api_admin.yaml`**（对接 edgeapi）：
```yaml
rpc.endpoints: [ "http://127.0.0.1:8031" ]
nodeId: "edgeapi 自动生成的 nodeId"
secret: "edgeapi 自动生成的 secret"
```

**`configs/server.yaml`**：
```yaml
env: prod
http:
  "on": true
  listen: [ "0.0.0.0:7788" ]
https:
  "on": false
```

首次启动前，`edgeAdmins` 表为空，手动插入一个管理员账号：

```bash
mysql -u root -p db_edge <<'SQL'
INSERT INTO edgeAdmins (username, password, fullName, isSuper, canLogin, state, createdAt)
VALUES ('admin', MD5('admin'), '管理员', 1, 1, 1, UNIX_TIMESTAMP());
SQL
```

> 生产环境务必把 `MD5('admin')` 换成 `MD5('强密码')`。

### dns-edge

**`Corefile.local`**（本机联调用；生产建议复制一份改名 `Corefile`，二者内容一致）：

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

    # NS 模式：连 edgeapi 的 gRPC，10s 轮询任务、同步 NSDomain/NSRecord
    edgeagent {
        endpoint  127.0.0.1:8031
        unique_id <edgeapi 里对应 NSNode 的 uniqueId>
        secret    <同一条 NSNode 记录的 secret>
    }

    # 地理路由（可选，CDN 模式用）
    geo {
        xdb             /path/to/ip2region.xdb
        auto_update     true
        update_interval 24h
    }
}
```

dns-edge 同时支持两种模式，可以只开一个也可以两个都开：
- **CDN 模式**（`api` 块）：edgeapi 通过 edgeDNSAPI 主动推送 CDN 记录，不需要
  `edgeagent` 块
- **NS 模式**（`edgeagent` 块）：dns-edge 主动拉取 NS 域名/记录，需要先在
  EdgeAdmin「智能DNS」里建好集群和节点，拿到节点的 `uniqueId`/`secret`

---

## 三、验证

```bash
# DNS 解析
dig @<dns-edge-ip> -p 5300 <域名> A +short

# API 健康检查
curl http://<dns-edge-ip>:8080/healthz
# → {"status":"ok","zoneCount":N}

# EdgeAdmin
curl -A "Mozilla/5.0" http://<edgeadmin-ip>:7788/
# 返回登录页 HTML；反爬虫规则会拦截没有 UA 或 UA 是 curl/wget/python 的请求
```

---

## 四、生产环境进程守护（systemd）

开发/联调机没有免密 sudo 时用「本地进程管理」（`make deploy-local` 那套）；
真正的生产环境建议走 systemd：

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

**`/etc/systemd/system/dns-edge.service`**——dns_dev 的 Makefile 已经有对应的
`make install`（会生成并启用这个 unit，路径按 `PREFIX ?= /opt/dns-edge` 展开），
不需要手写：

```bash
cd /home/<user>/dns_dev
sudo make install   # build + 安装二进制/配置/systemd unit + enable + start
```

```bash
systemctl daemon-reload
systemctl enable --now edge-api edge-admin dns-edge
```

---

## 五、常见问题

**Tea.Root 找不到配置 / 编译产物放哪都报配置缺失**

`Tea.Root` 由二进制**真实路径**决定：`filepath.Dir(filepath.Dir(可执行文件路径))`，
和进程当前工作目录（cwd）无关。二进制在 `<repo>/build/edge-api`，Tea.Root 就是
`<repo>`，`configs/` 必须和 `build/` 同级（即 `<repo>/configs/`），不是
`build/configs/`。

**edgeapi/edgeadmin 编译报"找不到某个 gitlab.gainetics.io/.../edgecommon 子包"**

`../edgecommon` 目录不存在，或者不在正确的相对位置——回到「目录结构约定」检查
`edgecommon` 是否和 `edgeapi`/`edgeadmin` 平级、分支是否也是
`feature/ns-dns-edge`。

**改了依赖之后突然编译报"缺少某个字段"（比如 WAF/CC 相关字段）**

八成是不小心把 `gitlab.gainetics.io/backend-cdn/goedge/edgecommon` 的 import
路径改成了 `github.com/TeaOSLab/EdgeCommon`——这两个名字长得像，但后者是从公共
`proxy.golang.org` 能下载到的**开源公版**，不是公司私有 fork，没有私有定制字段。
判断依据：私有 fork 走本地 `replace`，`go.sum` 里**没有**它的真实哈希；公版会被
公共 proxy 缓存，`go.sum` 里能查到正常条目。统一按
`gitlab.gainetics.io/backend-cdn/goedge/edge{common,admin}` 这条路径来。

**edgeadmin 编译报 cgo/C 相关错误**

edgeadmin 必须 `CGO_ENABLED=0` 编译（`internal/waf/injectionutils` 下有未加
build tag 的 `.c` 文件）。`make build`/`make deploy-local` 已经处理好这一点，
不要手动跳过 Makefile 直接跑 `go build`。

**dns-edge 重启后 NS 域名解析不出来 / REFUSED**

正常应该秒恢复：`edgeagent`（`internal/edgeagent/agent.go`）在每次连接建立后
（含进程刚启动、断线重连）都会无条件做一次全量 domain/record 同步，不需要等
EdgeAdmin 那边有人手动改了什么才触发。如果没恢复，先看 dns-edge 日志里
`edgeagent: connected to edgeapi`/`(re)established` 有没有打出来，没有说明
gRPC 连接本身有问题（检查 `edgeagent.endpoint`/`unique_id`/`secret` 是否正确）。

**dns-edge 重启后 CDN 模式记录丢失**

无需手动操作。edgeapi 的 `DNSTaskExecutor` 每 20 秒检测一次 dns-edge 的 domain
列表，发现为空时自动触发重推，实测 5–20 秒内记录自动恢复。超过 1 分钟未恢复，
可在 EdgeAdmin DNS 域名页手动点「同步」强制触发。

**`curl` 测试 EdgeAdmin 被 403**

反爬虫规则会拦截 UA 命中 `curl`/`wget`/`python` 的请求，测试时带上
`-A "Mozilla/5.0"`。登录表单的 CSRF token 不是服务端直接渲染的，是先
`GET /csrf/token` 异步拿到（单次消费、30 分钟有效），完整的 curl 自动化登录
流程见 `findings.md`。
