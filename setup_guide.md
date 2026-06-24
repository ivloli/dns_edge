# dns-edge 部署指南

本文档说明如何使用 Makefile 进行本地编译打包和目标机部署。

---

## 目录

- [快速开始](#快速开始)
- [本地编译打包](#本地编译打包)
- [目标机部署](#目标机部署)
- [服务管理](#服务管理)
- [自定义安装路径](#自定义安装路径)
- [卸载](#卸载)

---

## 快速开始

### 1. 本机打包

```bash
# 进入项目目录
cd /path/to/dns_dev

# 打包（使用默认配置文件 Corefile）
make release-package

# 打包测试环境（使用 Corefile.test，压缩包名称带 test 标识）
make release-package CONFIG_SRC=Corefile.test RELEASE_INFIX=test

# 打包生产环境
make release-package CONFIG_SRC=Corefile.prod RELEASE_INFIX=prod
```

生成的压缩包格式：`dns-edge-<infix>-linux-amd64-<git-tag>.tar.gz`

### 2. 传输到目标机

```bash
scp dns-edge-test-linux-amd64-*.tar.gz user@target-server:/tmp/
```

### 3. 目标机部署

```bash
# 解压
cd /tmp
tar -xzf dns-edge-test-linux-amd64-*.tar.gz
cd dns-edge-test-linux-amd64-*/

# 部署（默认安装到 /opt/dns-edge）
sudo make install
```

部署完成后服务自动启动，可通过 `sudo make status` 查看状态。

---

## 本地编译打包

### 基础用法

```bash
# 使用默认配置打包
make release-package
```

默认行为：
- 配置文件：`Corefile`（项目根目录）
- 目标平台：`linux/amd64`
- 压缩包名：`dns-edge-linux-amd64-<git-tag>.tar.gz`

### 指定配置文件

```bash
# 打包时使用 Corefile.test
make release-package CONFIG_SRC=Corefile.test

# 打包时使用 Corefile.prod
make release-package CONFIG_SRC=Corefile.prod
```

### 指定压缩包中缀

```bash
# 压缩包名带 test 标识：dns-edge-test-linux-amd64-xxx.tar.gz
make release-package RELEASE_INFIX=test

# 压缩包名带 prod 标识：dns-edge-prod-linux-amd64-xxx.tar.gz
make release-package RELEASE_INFIX=prod
```

### 指定目标平台

```bash
# 交叉编译 ARM64
make release-package TARGET_OS=linux TARGET_ARCH=arm64

# macOS
make release-package TARGET_OS=darwin TARGET_ARCH=amd64
```

### 完整示例

```bash
# 打包生产环境，ARM64 平台
make release-package \
  CONFIG_SRC=Corefile.prod \
  RELEASE_INFIX=prod \
  TARGET_OS=linux \
  TARGET_ARCH=arm64
# 生成: dns-edge-prod-linux-arm64-<git-tag>.tar.gz
```

### 生成校验和

```bash
# 打包并生成 SHA256 校验和
make release-checksum CONFIG_SRC=Corefile.test RELEASE_INFIX=test
```

---

## 目标机部署

### 默认部署（推荐）

解压后直接安装到 `/opt/dns-edge`：

```bash
tar -xzf dns-edge-test-linux-amd64-*.tar.gz
cd dns-edge-test-linux-amd64-*/
sudo make install
```

**安装后的目录结构：**

```
/opt/dns-edge/
├── bin/
│   └── dns-edge          # 二进制可执行文件
├── etc/
│   ├── Corefile          # 配置文件
│   └── env               # 环境变量文件（可选）
└── data/                 # 工作目录

/etc/systemd/system/
└── dns-edge.service      # systemd 服务单元
```

### 重复安装（覆盖升级）

`make install` 会自动：
1. 停止正在运行的 dns-edge 服务
2. 替换二进制文件
3. 覆盖配置文件（`Corefile`）
4. 重启服务

**升级流程：**

```bash
# 传输新版本压缩包到目标机
scp dns-edge-test-linux-amd64-new.tar.gz user@target:/tmp/

# 目标机操作
cd /tmp
tar -xzf dns-edge-test-linux-amd64-new.tar.gz
cd dns-edge-test-linux-amd64-*/
sudo make install  # 自动停止旧服务，替换文件，启动新服务
```

---

## 服务管理

部署后服务已自动启动，使用以下命令管理：

```bash
# 查看服务状态
sudo make status
# 或
sudo systemctl status dns-edge

# 启动服务
sudo make start

# 停止服务
sudo make stop

# 重启服务
sudo make restart

# 查看日志
sudo journalctl -u dns-edge -f

# 查看最近 100 行日志
sudo journalctl -u dns-edge -n 100
```

### 修改配置后重启

```bash
# 编辑配置文件
sudo vim /opt/dns-edge/etc/Corefile

# 重启服务使配置生效
sudo make restart
```

---

## 自定义安装路径

如果需要安装到非默认路径（比如 `/usr/local`），使用以下参数：

```bash
sudo make install \
  PREFIX=/usr/local \
  ETC_DIR=/etc/dns-edge \
  DATA_DIR=/var/lib/dns-edge
```

**安装后的路径：**
- 二进制：`/usr/local/bin/dns-edge`
- 配置：`/etc/dns-edge/Corefile`
- 数据：`/var/lib/dns-edge/`

**注意**：`make install` 会自动根据参数修改 systemd 服务文件中的路径，无需手动编辑 `dns-edge.service`。

---

## 卸载

```bash
cd /path/to/unpacked/release/
sudo make uninstall
```

卸载操作会：
1. 停止并禁用 dns-edge 服务
2. 删除 systemd 服务文件
3. 删除二进制文件

**配置文件不会被删除**，保留在 `/opt/dns-edge/etc/`（或自定义的 `ETC_DIR`），方便重新安装时恢复配置。

如需完全删除，手动执行：

```bash
sudo rm -rf /opt/dns-edge
```

---

## 开发者命令

### 本地构建

```bash
# 编译二进制到 bin/dns-edge
make build

# 运行单元测试
make test

# 清理编译产物
make clean

# 整理依赖
make tidy
```

### 本地安装（开发机）

```bash
# 从源码编译并安装到本机
sudo make install
```

---

## 故障排查

### 服务启动失败

```bash
# 查看详细错误日志
sudo journalctl -u dns-edge -n 50 --no-pager

# 检查配置文件语法
/opt/dns-edge/bin/dns-edge -config /opt/dns-edge/etc/Corefile --help
```

### 端口冲突

如果 DNS 端口（默认 53）或 API 端口（默认 8080）被占用：

```bash
# 检查端口占用
sudo ss -tulnp | grep -E ':(53|8080)'

# 编辑 Corefile 修改端口
sudo vim /opt/dns-edge/etc/Corefile
# 修改 listen 和 api 行
# 然后重启服务
sudo make restart
```

### PostgreSQL 连接失败

检查 `/opt/dns-edge/etc/Corefile` 中的 `postgres { dsn ... }` 配置：

```bash
# 使用 psql 测试连接
psql "postgres://user:pass@host:5432/dbname?sslmode=disable"
```

### Nacos 连接失败

检查防火墙是否允许访问 Nacos 端口（18848, 19848），并确认 `nacos { ... }` 块配置正确。

---

## 参数速查表

### 打包参数

| 参数 | 默认值 | 说明 | 示例 |
|------|--------|------|------|
| `CONFIG_SRC` | `Corefile` | 打包的配置文件 | `CONFIG_SRC=Corefile.test` |
| `RELEASE_INFIX` | _(空)_ | 压缩包中缀标识 | `RELEASE_INFIX=prod` |
| `TARGET_OS` | `linux` | 目标操作系统 | `TARGET_OS=darwin` |
| `TARGET_ARCH` | `amd64` | 目标架构 | `TARGET_ARCH=arm64` |

### 安装参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `PREFIX` | `/opt/dns-edge` | 安装根目录 |
| `ETC_DIR` | `$(PREFIX)/etc` | 配置目录 |
| `DATA_DIR` | `$(PREFIX)/data` | 数据目录 |

---

## 常见部署场景

### 场景 1：测试环境快速部署

```bash
# 本机打包
make release-package CONFIG_SRC=Corefile.test RELEASE_INFIX=test

# 传输并部署
scp dns-edge-test-*.tar.gz test-server:/tmp/
ssh test-server "cd /tmp && tar -xzf dns-edge-test-*.tar.gz && cd dns-edge-test-* && sudo make install"
```

### 场景 2：生产环境部署（多节点）

```bash
# 本机打包生产版本
make release-package CONFIG_SRC=Corefile.prod RELEASE_INFIX=prod

# 批量部署到多台服务器
for host in prod-dns-01 prod-dns-02 prod-dns-03; do
  scp dns-edge-prod-*.tar.gz $host:/tmp/
  ssh $host "cd /tmp && tar -xzf dns-edge-prod-*.tar.gz && cd dns-edge-prod-* && sudo make install"
done
```

### 场景 3：灰度升级

```bash
# 打包新版本
make release-package CONFIG_SRC=Corefile.prod RELEASE_INFIX=prod

# 先升级一台验证
scp dns-edge-prod-*.tar.gz canary-server:/tmp/
ssh canary-server "cd /tmp && tar -xzf dns-edge-prod-*.tar.gz && cd dns-edge-prod-* && sudo make install"

# 验证无误后，滚动升级其他节点
# ...
```

---

## 更多信息

- **测试文档**：[docs/test-plan.md](docs/test-plan.md)
- **冒烟测试**：[docs/smoke-test.md](docs/smoke-test.md)
- **技术设计**：[docs/technical-design.md](docs/technical-design.md)
- **项目 README**：[README.md](README.md)
