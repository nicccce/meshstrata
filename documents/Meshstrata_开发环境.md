# Meshstrata｜开发环境

**安装日期：2026-11-09（Asia/Shanghai）｜机器：Ubuntu 26.04.1 LTS / Linux amd64**

依据[开发日志](Meshstrata_开发日志.md)中的已确认方案准备本机环境。当前仓库只有设计文档，尚未创建 Controller、Agent 或前端工程；本次环境验证使用临时工程，不代表产品功能已经实现。

## 已安装工具

| 工具 | 版本 | 安装位置 / 命令 |
| --- | --- | --- |
| Go | 1.27.3 | `/opt/meshstrata-dev/go1.27.3`；`go`、`gofmt` |
| Node.js | 26.13.0 LTS | `/opt/meshstrata-dev/node-v26.13.0-linux-x64`；`node` |
| npm | 11.20.0 | 随上述 Node.js 安装；`npm`、`npx` |
| sqlc | 1.31.1 | `/opt/meshstrata-dev/sqlc-v1.31.1/sqlc`；`sqlc` |
| SQLite CLI | 3.46.1（Ubuntu 包 `3.46.1-9ubuntu0.3`） | `/usr/bin/sqlite3` |
| 项目测试 Mihomo | 1.19.33，linux-amd64-v1 | `/usr/local/libexec/meshstrata/mihomo-v1.19.33`；`meshstrata-mihomo` |

Go、Node.js、npm、npx、gofmt、sqlc 和项目测试 Mihomo 的命令链接位于 `/usr/local/bin`，该目录已经在 PATH 中，无需手动激活环境。Git、build-essential、CA 证书、curl、xz 和 OpenSSL 使用机器已有安装。

SQLite CLI 用于开发时检查数据库；应用仍采用 `modernc.org/sqlite`，不依赖系统 SQLite 库或 CGO。Vue、Vite、TypeScript、Naive UI、Vue I18n 和 Go 库由后续工程的依赖文件管理，不作为全局命令安装。ACME 的 lego 按设计作为 Go 库评估，本次不安装独立的证书服务。

## 下载与校验

Go 使用[官方发布元数据](https://go.dev/dl/?mode=json)中的 SHA-256；Node.js 使用[官方校验清单](https://nodejs.org/dist/v26.13.0/SHASUMS256.txt)；[sqlc](https://github.com/sqlc-dev/sqlc/releases/tag/v1.31.1) 和 [Mihomo](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.33) 使用各自官方 GitHub Release API 提供的 SHA-256。四个下载包均在安装前通过校验。

| 下载包 | SHA-256 |
| --- | --- |
| `go1.27.3.linux-amd64.tar.gz` | `65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40` |
| `node-v26.13.0-linux-x64.tar.xz` | `d28a96af6c38596f6d9d97c0ee531c13d238d757742387d1928de822d959e997` |
| `sqlc_1.31.1_linux_amd64.tar.gz` | `497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354` |
| `mihomo-linux-amd64-v1-v1.19.33.gz` | `8ec92e7b2fdc6b853ba00156bab7e4e965c8b0d843b8c4ac8ab98795f02ad524` |

本机另存下载地址与摘要到 `/opt/meshstrata-dev/install-manifest.json`。版本反映本次安装选择，后续升级需要重新验证。

系统原有阿里云 HTTP 软件源更新无响应；本次通过临时 APT sources 文件使用 Ubuntu 官方 HTTPS 源安装 `sqlite3`，使用 Ubuntu archive keyring 验证软件包，未修改 `/etc/apt/sources.list.d/ubuntu.sources`。

## Mihomo 共存

机器原有 `/usr/local/bin/mihomo` 及其运行实例保留，项目测试程序通过 `meshstrata-mihomo` 单独调用。没有安装或启用 Meshstrata systemd 服务，也没有启动新的常驻代理实例。

开发测试必须显式提供独立工作目录和完整配置，再执行校验：

```bash
meshstrata-mihomo -v
meshstrata-mihomo -t -d /absolute/path/to/test-state -f /absolute/path/to/config.yaml
```

配置继续遵守开发日志中的约束：关闭通用入站、TUN 和自动 iptables；DNS 不监听宿主机端口；管理 API 使用专属 Unix socket，且其父目录限制访问。固定 v1.19.33 是兼容性验证基线，尚未通过四协议和完整发布流程验收，不代表已经确定生产交付内核。

## 环境验证

临时验证工程位于 `/tmp/meshstrata-dev-check`，可以清理，不属于项目源码。验证期间下载的依赖保留在 npm / Go 默认缓存中。

前端使用下列精确版本完成 `vue-tsc --noEmit && vite build`：

| 依赖 | 版本 |
| --- | --- |
| Vue | 3.5.45 |
| TypeScript | 6.0.3 |
| Vite | 8.3.6 |
| @vitejs/plugin-vue | 6.0.10 |
| vue-tsc | 3.3.14 |
| Naive UI | 2.45.3 |
| Vue I18n | 11.4.18 |

后端验证组合为 Go 1.27.3、sqlc 1.31.1 和 `modernc.org/sqlite` 1.62.0；使用 sqlc 从 SQLite schema/query 生成 `database/sql` 代码，并在 `CGO_ENABLED=0` 下编译。具体检查包括 SQLite 插入与查询，以及通过 `go:embed` 嵌入前端构建产物并用 `net/http/httptest` 验证 HTTP 响应。

上述前后端检查全部通过；SQLite CLI 的内存数据库插入、查询和 `PRAGMA integrity_check` 也通过。

Mihomo 使用完整的隔离配置执行 `-t` 校验，通用代理端口均为 0，不启动监听。完整节点部署、协议转发、热重载、撤销和到期行为留待实现后验收。

常用环境检查：

```bash
go version
node --version
npm --version
sqlc version
sqlite3 --version
meshstrata-mihomo -v
```

本机约 1.7 GiB 内存；首次编译 Go 依赖时可用 `GOMAXPROCS=2 go build -p 2 ./...` 限制并发。本次验证使用该设置，没有修改全局 Go 配置。下一步按开发日志建立最小工程，并在 `go.mod`、`package.json` 和锁文件中固定项目依赖。
