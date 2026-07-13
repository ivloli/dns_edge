---
name: deploy-services
description: 打包 edgeapi/edgeadmin/dns-edge 并生成贵州+新加坡测试环境的部署命令。触发词："打包部署"、"生成部署命令"、"打包测试环境"、"部署到测试环境"、"重新部署"。用户明确要求打包/生成部署命令时使用，不要在只是问"怎么部署"这种纯咨询场景下主动触发。
user-invocable: true
allowed-tools: "Bash Read Write"
---

# 打包 + 生成部署命令

这个 skill 把"打包 edgeapi/edgeadmin/dns-edge 三个服务，然后生成贵州/新加坡测试环境部署命令"这套本项目反复要做的流程固化下来，不用每次从头想。

## 背景知识（本项目固定拓扑，除非用户说变了否则直接用）

**四个仓库**：
- `dns_dev`（本仓库）——dns-edge，公共GitHub仓库，我可以直接push
- `/home/ivloli/Git_repo/edgeapi`——私有GitLab仓库
- `/home/ivloli/Git_repo/edgeadmin`——私有GitLab仓库
- `/home/ivloli/Git_repo/edgecommon`——私有GitLab仓库，共享proto/model，通常改动edgeapi/edgeadmin时如果没碰proto就不需要动它

**分支约定**：三个仓库（edgeapi/edgeadmin/dns_dev）统一用 `feature/ns-dns-edge`，edgeapi/edgeadmin 还要保持一份跟它完全同步的 `test` 分支（详见下方"git commit/push"一节）。

**测试环境拓扑**（部署命令要覆盖的目标）：

| 服务 | 机器 | 路径 | 端口 | 二进制位置 |
|------|------|------|------|-----------|
| edgeapi | 新加坡（Singapore） | 视实际情况，找 `bin/edge-api` 或根目录 | gRPC `:8031`（对外可能是`:8001`，问用户或看`ss` 结果确认） | 通常 `bin/edge-api` |
| edge-admin | 贵州 | `/data/go-edge/edge-admin` | HTTP `:7788` | `bin/edge-admin` + `web/` 目录整体替换 |
| dns-edge 实例1 | 贵州 | `/data/go-edge/dns-edge` | DNS `:5300`，API `:8080` | `bin/dns-edge`（在bin/子目录下，跟实例2/3不同） |
| dns-edge 实例2 | 贵州（同一台，跟实例1同机） | `/data/go-edge/dns-edge-instance2` | DNS `:5301`，API `:8081` | `dns-edge`（直接在根目录，没有bin/子目录） |
| dns-edge 实例3 | 贵州（同一台） | `/data/go-edge/dns-edge-instance3` | DNS `:5302`，API `:8082` | `dns-edge`（根目录） |

**edgenode 通常不需要打包**——除非这次改动明确碰了CDN边缘节点代码（跟NS/DoT/DoH/SOA这类改动通常无关）。

## 执行步骤

### 1. 确认改动范围和分支状态

先检查这三个仓库改了什么、在哪个分支：
```bash
for repo in dns_dev "Git_repo/edgeapi" "Git_repo/edgeadmin"; do
  echo "=== $repo ==="
  git -C "/home/ivloli/$repo" branch --show-current
  git -C "/home/ivloli/$repo" log --oneline -3
  git -C "/home/ivloli/$repo" status --short
done
```

如果这次改动**只碰了dns-edge**（比如纯dns-edge侧的bug修复），只打包dns-edge一个服务，不要无脑打包全部三个——问清楚或者根据 `git log`/`git diff` 判断改动范围。

### 2. git commit（如果还没commit）

用户明确要求commit时才做，遵循仓库已有的commit message风格（`feat(ns): ...`/`fix(...): ...`）。**只 `git add` 明确改过的文件**，不要 `git add -A`——这三个仓库（尤其edgeadmin）工作区里经常有不属于这次改动的既有未追踪文件/目录（比如 `web/public/public`、`web/views.bak/`、`web/views/views` 这几个已知的构建产物/备份目录，`dns_dev` 里的 `task_plan.md`/`progress.md`/`findings.md`/`deploy/` 等规划文档），扫进commit容易把不相关的东西也提交了。

### 3. git push——feature 分支 + test 分支都要

**dns_dev**：远程是公共GitHub仓库（`upstream`），可以直接push：
```bash
cd /home/ivloli/dns_dev && git push upstream feature/ns-dns-edge
```

**edgeapi/edgeadmin**：远程是私有GitLab仓库。这两个仓库的 `feature/ns-dns-edge` 和 `test` 分支要保持完全同步：
```bash
for repo in edgeapi edgeadmin; do
  dir="/home/ivloli/Git_repo/$repo"
  git -C "$dir" checkout test
  git -C "$dir" merge --ff-only feature/ns-dns-edge   # 通常能直接fast-forward；如果不能说明test分支有额外提交，先跟用户确认怎么处理，不要强推
  git -C "$dir" checkout feature/ns-dns-edge
done
```
然后各自 `git push origin feature/ns-dns-edge` 和 `git push origin test`。

**已知的坑**：这两个私有仓库的push经常被自动安全分类器误判成"目标是公共仓库"而拦截（哪怕 `git remote -v` 明确显示是私有GitLab）。**遇到这个不要反复重试push本身**——把准确的commit hash和push命令列出来，请用户自己在真实终端执行，之后用 `git fetch origin <branch>` + 比对 `git rev-parse <branch>` / `git rev-parse origin/<branch>` 这类只读操作帮用户核实是否真的推送成功（只读操作不会被同一个分类器拦）。

### 4. 打包

```bash
cd /home/ivloli/Git_repo/edgeapi && make package
cd /home/ivloli/Git_repo/edgeadmin && make package
cd /home/ivloli/dns_dev && make release-package
```

产物：
- `edgeapi/edge-api-test-env.tar.gz`
- `edgeadmin/edge-admin-test-env.tar.gz`
- `dns_dev/dns-edge-linux-amd64-<commit短hash>.tar.gz`

**注意**：`make package`/`make release-package` 会把 `build/edge-api`/`build/edge-admin`/`bin/dns-edge` 这几个路径**原地重新编译**——如果本机同时有用同一路径跑着的本地开发/联调进程，重新编译不会打断它（Go编译产物是原子rename替换，运行中进程持有旧inode继续跑），但如果内容跟运行中的不一样，进程本身不会自动重启用新代码，这是正常的，不用特意处理。

把三个产物收集到一个时间戳目录方便交付：
```bash
RELDIR=/home/ivloli/dns_dev/release-artifacts/$(date +%Y%m%d-%H%M%S)
install -d -m 755 "$RELDIR"
cp /home/ivloli/dns_dev/dns-edge-linux-amd64-*.tar.gz "$RELDIR/"
cp /home/ivloli/Git_repo/edgeapi/edge-api-test-env.tar.gz "$RELDIR/"
cp /home/ivloli/Git_repo/edgeadmin/edge-admin-test-env.tar.gz "$RELDIR/"
sha256sum "$RELDIR"/*.tar.gz
```

### 5. 部署前检查清单（每次都要过一遍，不能跳）

1. **MySQL 表结构要不要改**——对照这次改动是否新增/修改了数据库列（查 `sql.json` 的git diff，或者看有没有新的 `Update*`/`Find*` DAO方法读写了新列）。如果需要，给出精确的 `ALTER TABLE` 语句，并提醒：`teaconst.Version` 没跟着涨的话 `autoUpgrade()` 不会自动迁移，线上库需要手动执行。
2. **已知遗留问题要不要一起提**——比如 `edgeIPLibraryArtifacts.filename` 缺默认值这个坑（`ALTER TABLE edgeIPLibraryArtifacts MODIFY COLUMN filename varchar(255) NOT NULL DEFAULT '';`），如果目标库还没修过，部署edgeapi前提醒用户先跑。
3. **新功能是不是默认关闭、需要额外去EdgeAdmin界面开启**——很多这类功能（SOA、Hosts、TLS、DoH）升级完二进制不会自动生效，要提醒用户去对应设置页面配置。

### 6. 生成部署命令

**核心原则：杀老进程直接用 `fuser -k` 对着文件本身操作，不要猜PID**——这是踩了三种不同PID识别方式的坑之后，最终收敛出来的最鲁棒方案：

1. `ss -tlnp | grep pid=` 解析失败过（某台机器上`ss`没输出预期格式），导致 `$OLD_PID` 是空字符串，`kill ""` 报错但脚本没检查就往下走，老进程没死，`cp` 覆盖正在运行的二进制报 `Text file busy`，新进程因为检测到老进程的本地锁而立刻退出——整个升级静默失败但看着像是成功了。
2. `pgrep -f "dns-edge-instance3/dns-edge"` 这种"假设目录名会出现在进程命令行里"的匹配也失败过——如果进程是 `cd` 进目标目录后用 `./dns-edge -config Corefile` 相对路径启动的，`cmdline`（`pgrep -f`匹配的对象）里根本不包含目录名，**工作目录不算进cmdline**。三个dns-edge实例如果都是同样方式启动，cmdline会完全一样，`pgrep -f "dns-edge"` 没法区分到底是哪一个，`head -1` 可能杀错/找不到目标实例。
3. 用 `/proc/<pid>/cwd` 反查归属目录理论上更可靠，但现场排查这个要一步步来，用户等得不耐烦——最后还是 `fuser -k` 一次成功，干脆把它定成默认方案。

```bash
cd <服务目录>

# 直接对二进制文件本身操作，不需要知道PID、不需要担心cmdline/cwd匹配问题
fuser -k -9 <二进制路径>
sleep 2
fuser <二进制路径> 2>&1 || echo "确认没人占用了"
ss -tlnp | grep ":<PORT> " || echo "端口已释放"

cp <二进制路径> <二进制路径>.bak.$(date +%Y%m%d%H%M%S)
cp <新二进制来源> <二进制路径>
chmod +x <二进制路径>

# ⚠️ 立刻核对一下二进制真的换成新的了，见下面"哈希验证"一节——
# 不要等启动完、测完功能才发现二进制根本没换
sha256sum <二进制路径>

# edge-admin 额外要整体替换 web/ 目录（先备份）：
#   cp -r web web.bak.$(date +%Y%m%d%H%M%S)
#   rm -rf web && cp -r <新web目录> web

nohup <启动命令> >> <日志文件> 2>&1 &
sleep 2  # dns-edge建议sleep 3
ps aux | grep "[对应的grep过滤模式]"
ss -tlnp | grep ":<PORT> "
tail -15 <日志文件>
```

**如果这台机器没有 `fuser` 命令**（不常见，但有的精简镜像没装），退回按 `/proc/<pid>/cwd` 反查（同一台机器上有多个同名进程、无法靠cmdline区分时必须用这个，不能瞎猜）：

```bash
find_pid_by_cwd() {
    local target
    target=$(readlink -f "$1")
    for pid in $(pgrep -f "<进程名关键字，比如 dns-edge>"); do
        if [ "$(readlink -f "/proc/$pid/cwd" 2>/dev/null)" = "$target" ]; then
            echo "$pid"
            return
        fi
    done
}
OLD_PID=$(find_pid_by_cwd "$(pwd)")
echo "OLD_PID=$OLD_PID"
if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do kill -0 "$OLD_PID" 2>/dev/null || break; sleep 1; done
    kill -0 "$OLD_PID" 2>/dev/null && { kill -9 "$OLD_PID"; sleep 1; }
fi
```

### 哈希验证：压缩包的sha256 和 解压后二进制的sha256，是两个不同的值，千万别搞混

这个错我自己在贵州实例1那次部署里真的犯过一次，害用户以为升级又失败了，白白多走一轮排查——**`dns-edge-linux-amd64-<commit>.tar.gz` 压缩包本身的sha256**，跟**这个压缩包解压出来那个 `dns-edge` 可执行文件的sha256**，是两个完全独立、毫无关系的值。生成部署命令时必须**分别算出这两个值、分别标注期望值**，不能图省事只算一个就当成两者通用：

```bash
# 本机打包完，两个值都要留：
sha256sum dns-edge-linux-amd64-<commit>.tar.gz          # 压缩包本身的哈希——验证"传输有没有损坏"用
tar -xzf dns-edge-linux-amd64-<commit>.tar.gz -C /tmp/verify
sha256sum /tmp/verify/dns-edge-linux-amd64-<commit>/dns-edge   # 解压后二进制的哈希——验证"cp换的是不是这个新文件"用
```

给用户的部署命令里，"验证传输"和"验证升级生效"这两步要分别标清楚期望的是哪个哈希，不要笼统写一句"期望值：xxx"了事。

**dns-edge 三个实例的关键区别**（容易搞混，务必核对）：
- 实例1二进制在 `bin/dns-edge`，实例2/3在根目录 `dns-edge`（没有bin子目录）——路径要对应改
- 三个实例的 dns-edge 二进制其实是**同一份**（`tar -xzf dns-edge-linux-amd64-<commit>.tar.gz` 解压一次，取出 `dns-edge` 二进制，三个实例分别cp过去用），不需要为每个实例单独打包
- **绝对不能**把整个tarball直接解压覆盖到实例目录——tarball里打包的 `Corefile` 是仓库自带的通用开发配置（没有真实的AccessKey/edgeagent凭证），只应该取出二进制文件，实例目录里已有的真实 `Corefile` 原封不动
- **如果升级过程中出现 `cp: ... Text file busy`**，说明老进程没被正确杀掉——直接 `fuser -k -9 <二进制路径>` 一次到位，不要再去猜PID；顺带确认一下有没有因为之前失败的尝试留下绑定失败的僵尸新进程（`ps aux | grep dns-edge`看有没有多余的、`ps -p <pid> -o etime`看存活时长），一并清理掉
- 部署完用 `sha256sum` 核实二进制真的换了之后，**不要在部署这台机器本机上用 `dig` 自己的公网IP做功能验证**（尤其是ECS/地理路由相关功能）——同机自连流量大多数情况下走本地环回路由，dns-edge看到的源地址是`127.0.0.1`，不是真实对外IP，会得出"看起来还是不对"的错误结论。必须换一台真正在公网另一端的机器发起查询才能验证到位。

**edge-admin/edge-api 同理**——tarball里的 `configs/*.template.yaml` 都是占位模板，只取二进制（+ edge-admin 的 `web/` 静态资源目录，这个不含凭证可以整体替换），目标机器上已有的真实配置文件不要碰。

按上表列出的拓扑，依次生成：新加坡机器 edgeapi 一段 → 贵州机器（dns-edge解压一次 + edge-admin一段 + 三个dns-edge实例各一段）。

## 完整示例（2026-07-13 DoT/DoH 功能部署，供参照）

这是一次真实跑通的完整流程，commit `1d22675`（dns_dev）/ `78109f95`（edgeapi）/ `eee2a744`（edgeadmin），可以当模板照抄，把 hash/sha256/路径换成当次实际值。

**打包**（步骤4的产物）：
```
dns-edge-linux-amd64-1d22675.tar.gz : 4cc9046fcb9b70cafe67d169a572d88288e596e6f84e475ba8c115ac95330a8c
edge-api-test-env.tar.gz            : 5d61d9e8fe241782f26cdef09cceef2e4a4ae7a7f39e4d7ec480b046296f32ed
edge-admin-test-env.tar.gz          : e58dde2ea935628d0d735cad1e2000f49bfcf1012e32140fe7e9cff79e2ef433
```

**部署前检查结论**（这次实际判断出来的，仅供参照，每次都要重新判断）：DoT/DoH复用了已有的 `edgeNSClusters.tls`/`.doh` 列，**不需要ALTER TABLE**；两个功能默认关闭，部署后要提醒用户去EdgeAdmin对应NS集群的"TLS"/"DoH"设置页面上传证书、开启开关才会生效。

**完整部署命令**（`pgrep -f` 版本，第一次用 `ss -tlnp | grep pid=` 那版在真实环境里翻车过——见下方"踩过的坑"）：

```bash
### 1. 新加坡机器：edgeapi（gRPC :8031）
cd /data/go-edge-test/edge-api   # 按实际路径调整

pgrep -af "bin/edge-api"
OLD_PID=$(pgrep -f "bin/edge-api" | head -1)
echo "OLD_PID=$OLD_PID"

if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do
        kill -0 "$OLD_PID" 2>/dev/null || break
        sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && { echo "强制kill -9"; kill -9 "$OLD_PID"; sleep 1; }
else
    echo "没找到运行中的edge-api，先别往下走，检查路径/进程名"
fi

ps aux | grep '[e]dge-api'
ss -tlnp | grep 8031 || echo "8031已释放"

cp bin/edge-api bin/edge-api.bak.$(date +%Y%m%d%H%M%S)
cp ~/upgrade-staging/edge-api-latest/edge-api bin/edge-api
chmod +x bin/edge-api

nohup ./bin/edge-api >> logs/run.log 2>&1 &
sleep 2
ps aux | grep '[e]dge-api'
ss -tlnp | grep 8031
tail -15 logs/run.log


### 2. 贵州机器：解压 dns-edge 包一次，三个实例共用
cd ~/upgrade-staging
sha256sum dns-edge-linux-amd64-1d22675.tar.gz
# 期望：4cc9046fcb9b70cafe67d169a572d88288e596e6f84e475ba8c115ac95330a8c

rm -rf dns-edge-latest
mkdir -p dns-edge-latest
tar -xzf dns-edge-linux-amd64-1d22675.tar.gz -C dns-edge-latest
NEW_BIN=~/upgrade-staging/dns-edge-latest/dns-edge-linux-amd64-1d22675/dns-edge


### 3. edge-admin（/data/go-edge/edge-admin）
cd /data/go-edge/edge-admin
sha256sum ~/upgrade-staging/edge-admin-test-env.tar.gz
# 期望：e58dde2ea935628d0d735cad1e2000f49bfcf1012e32140fe7e9cff79e2ef433

rm -rf ~/upgrade-staging/edge-admin-latest
mkdir -p ~/upgrade-staging/edge-admin-latest
tar -xzf ~/upgrade-staging/edge-admin-test-env.tar.gz -C ~/upgrade-staging/edge-admin-latest

OLD_PID=$(pgrep -f "bin/edge-admin" | head -1)
echo "OLD_PID=$OLD_PID"
if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do
        kill -0 "$OLD_PID" 2>/dev/null || break
        sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && { kill -9 "$OLD_PID"; sleep 1; }
else
    echo "没找到运行中的edge-admin，检查路径"
fi
ps aux | grep '[e]dge-admin'
ss -tlnp | grep 7788 || echo "7788已释放"

cp bin/edge-admin bin/edge-admin.bak.$(date +%Y%m%d%H%M%S)
cp -r web web.bak.$(date +%Y%m%d%H%M%S)
cp ~/upgrade-staging/edge-admin-latest/edge-admin bin/edge-admin
chmod +x bin/edge-admin
rm -rf web
cp -r ~/upgrade-staging/edge-admin-latest/web web

nohup ./bin/edge-admin >> logs/run.log 2>&1 &
sleep 2
ps aux | grep '[e]dge-admin'
ss -tlnp | grep 7788
tail -15 logs/run.log


### 4. dns-edge 实例1（/data/go-edge/dns-edge，:5300，二进制在 bin/）
cd /data/go-edge/dns-edge
OLD_PID=$(pgrep -f "bin/dns-edge" | head -1)
echo "OLD_PID=$OLD_PID"
if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do
        kill -0 "$OLD_PID" 2>/dev/null || break
        sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && { kill -9 "$OLD_PID"; sleep 1; }
else
    echo "没找到运行中的实例1，检查路径"
fi
ps aux | grep '[d]ns-edge'
ss -tlnp | grep -E ':5300|:8080' || echo "端口已释放"

cp bin/dns-edge bin/dns-edge.bak.$(date +%Y%m%d%H%M%S)
cp "$NEW_BIN" bin/dns-edge
chmod +x bin/dns-edge

nohup ./bin/dns-edge -config Corefile >> logs/run.log 2>&1 &
sleep 3
ps aux | grep '[d]ns-edge'
ss -tlnp | grep -E ':5300|:8080'
tail -20 logs/run.log
curl -s http://127.0.0.1:8080/healthz


### 5. dns-edge 实例2（/data/go-edge/dns-edge-instance2，:5301，二进制在根目录）
cd /data/go-edge/dns-edge-instance2
OLD_PID=$(pgrep -f "dns-edge-instance2/dns-edge" | head -1)
echo "OLD_PID=$OLD_PID"
if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do
        kill -0 "$OLD_PID" 2>/dev/null || break
        sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && { kill -9 "$OLD_PID"; sleep 1; }
else
    echo "没找到运行中的实例2，检查路径"
fi
ss -tlnp | grep -E ':5301|:8081' || echo "端口已释放"

cp dns-edge dns-edge.bak.$(date +%Y%m%d%H%M%S)
cp "$NEW_BIN" dns-edge
chmod +x dns-edge

nohup ./dns-edge -config Corefile >> run.log 2>&1 &
sleep 3
ss -tlnp | grep -E ':5301|:8081'
tail -20 run.log
curl -s http://127.0.0.1:8081/healthz


### 6. dns-edge 实例3（/data/go-edge/dns-edge-instance3，:5302，二进制在根目录）
cd /data/go-edge/dns-edge-instance3
OLD_PID=$(pgrep -f "dns-edge-instance3/dns-edge" | head -1)
echo "OLD_PID=$OLD_PID"
if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID"
    for i in $(seq 1 10); do
        kill -0 "$OLD_PID" 2>/dev/null || break
        sleep 1
    done
    kill -0 "$OLD_PID" 2>/dev/null && { kill -9 "$OLD_PID"; sleep 1; }
else
    echo "没找到运行中的实例3，检查路径"
fi
ss -tlnp | grep -E ':5302|:8082' || echo "端口已释放"

cp dns-edge dns-edge.bak.$(date +%Y%m%d%H%M%S)
cp "$NEW_BIN" dns-edge
chmod +x dns-edge

nohup ./dns-edge -config Corefile >> run.log 2>&1 &
sleep 3
ss -tlnp | grep -E ':5302|:8082'
tail -20 run.log
curl -s http://127.0.0.1:8082/healthz
```

**踩过的坑（第一版命令用 `ss` 解析PID，在新加坡机器上翻车的真实案例）**：

```
-bash: kill: `': not a pid or valid job spec
cp: cannot create regular file 'bin/edge-api': Text file busy
[1] 1625544
[1]+  Done   nohup ./bin/edge-api >> logs/run.log 2>&1
... start local sock failed: error: the process is already running, pid: 1520946
```

根因链条：`OLD_PID=$(ss -tlnp | grep ':8031 ' | grep -oP 'pid=\K[0-9]+' | head -1)` 在那台机器上没提取到PID（`ss` 输出格式或权限问题，没深究）→ `$OLD_PID` 是空字符串 → `kill ""` 直接报错但脚本没有检查这个失败就继续往下走 → 老进程（`1520946`）一直没死 → `cp` 覆盖正在运行的二进制被内核拒绝（`Text file busy`）→ 新启动的进程检测到老进程的本地sock锁，打印错误后自己退出 → **表面上看着流程都跑完了，实际上老进程从头到尾没被换掉，新代码根本没生效**。这是这个skill坚持"每一步都先检查OLD_PID是不是空、kill之后要循环确认真的退出"的直接原因，不是过度设计。

## 已知不在本skill范围内的事

- 不负责往 `test` 分支之外的其他分支同步
- 不负责实际SSH到目标机器执行——命令生成出来交给用户自己在真实终端跑
- 不负责判断"这次改动到底需不需要打包"——如果用户只是问一般性问题、没有明确要求打包部署，不要主动触发这个skill
