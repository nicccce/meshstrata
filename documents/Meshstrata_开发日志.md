# Meshstrata｜开发日志

**开发约定：每一轮代码、配置或文档修改完成并通过相应检查后，都必须创建 Git 提交，并推送到当前工作分支对应的远端分支。推送失败时保留本地提交，明确说明失败原因及当前状态；确认推送成功后才能报告已完成推送。**

## 已确认方案：A 方案 + 独立 Mihomo

**确认日期：2026-11-09（Asia/Shanghai）｜状态：基础工程已搭建，节点控制与代理业务待实现**

本项目采用 **Go + Vue 3 + TypeScript + Vite + SQLite**。Mihomo 作为随安装器交付的独立程序运行，由 Agent 通过本机管理 API 管理。设计目标是轻量、低常驻资源、易部署，并使 Agent 的更新与代理转发进程相互独立。

产品需求以 [Meshstrata_产品设计_v1.0.md](Meshstrata_产品设计_v1.0.md) 为依据；该文件正文当前版本为 **v1.0｜2026-11-09**。开发日志记录技术决策、实现进展和验证结果，不替代产品验收要求。

### 1. 技术栈

| 层面 | 选型 | 用途与约束 |
| --- | --- | --- |
| Controller | Go，HTTP 优先使用标准库 `net/http` | 单进程提供 Web 静态资源、业务 API、订阅下载、发布调度和节点控制通道。 |
| Agent | Go | 管理本机内核配置、按需探测、运行校验、凭据到期、证书维护和日志清理。 |
| 前端 | Vue 3 + TypeScript + Vite | 构建静态 SPA，通过 Go `embed` 随 Controller 发布；生产服务器无需运行 Node.js。 |
| 界面组件 | Naive UI，按需使用 | 实现表格、协议表单、线路向导、侧栏和发布状态。 |
| 国际化 | Vue I18n | 提供英文与简体中文资源；组件使用资源键，稳定 ID、配置字段、协议语法和代理组标识不翻译，切换不改写对象名称或历史数据。 |
| Controller 数据库 | SQLite + `modernc.org/sqlite` | 数据保存在本机持久目录，不引入独立数据库服务；驱动不依赖 CGO，便于构建发布包。 |
| 数据访问 | 显式 SQL + sqlc | 管理配置、依赖、订阅身份、测量缓存和发布任务；使用事务维护引用与状态一致性。 |
| 节点控制通道 | WebSocket + JSON over mTLS | Agent 主动连接 Controller，传递任务与结果；空闲时只保留必要心跳和重连。 |
| 内部身份 | Go TLS/X.509 + 内部管理 CA | 一次性入网 token、节点独立证书、续期与撤销。 |
| 公网证书 | 成熟 ACME 实现，优先评估 lego | 首接Cloudflare DNS-01并保留证书导入；Controller 管理 DNS API 凭据，节点私钥留在节点。具体 CSR 集成在实现阶段验证。 |
| 代理内核 | 独立 Mihomo，固定版本 | 初始以产品文档核对的 v1.19.33 为兼容性基线；完成验证后才能确定首个交付版本。 |
| 部署 | Linux预编译二进制 + systemd/OpenRC | Ubuntu与Alpine均为正式平台，三个角色及可选frp并列验收；安装器留B17，当前未实现生产部署。 |

首版不引入 Redis、消息队列、时序数据库或 Kubernetes。缓存优先使用进程内缓存和 SQLite；可恢复发布任务保存在数据库，由进程内受限并发的 worker 执行。单管理员登录采用服务端会话，不引入外部身份服务。

### 2. 组件与进程边界

```text
Browser
  └── HTTPS → Controller
                ├── Embedded Web Assets
                ├── API / Subscriptions / Release Tasks
                ├── SQLite
                └── mTLS WebSocket ← Agent
                                      ├── Local State / Expiry / Certificates
                                      ├── Config Generation / Verification
                                      └── Local API → Mihomo
                                                        └── Proxy Traffic
```

- Controller 与 Agent 独立编译、独立运行；Controller 首版不内置常驻 Agent 功能。需要同机承担代理节点职责时，由安装器额外安装独立 Agent 与 Mihomo。
- 本机节点与远端节点沿用相同的入网、mTLS 控制通道、发布校验、到期撤销和回滚机制，不建立特殊的本机管理分支；Controller 与 Agent 可共享 Go 数据模型、校验与证书工具包。
- 每台代理节点运行一个 Agent 和一个 Mihomo 实例；多条线路使用同一实例中的 Listener、Proxy 和规则，不按线路启动内核进程。
- Controller、Agent 与 Mihomo 分别使用 `meshstrata-controller.service`、`meshstrata-agent.service`、`meshstrata-mihomo.service`。systemd或OpenRC监督器负责保活；Agent 负责配置、健康确认及显式的内核升级、回滚操作，避免多个组件竞争管理进程。
- 普通 Controller 更新、停止或重启不联动停止 Agent 或 Mihomo，本机节点仍由 Agent 执行到期处理；权限和状态目录按职责分离。
- 普通 Agent 更新或重启不联动停止 Mihomo；内核升级、内核崩溃或 Listener 重建仍可能影响代理连接。
- 首版节点安装由安装器同时安装 Agent 与 Mihomo，不把 Mihomo 源码链接进 Agent，也不要求用户手动安装和配置内核。

OpenRC服务使用对应无`.service`后缀名称，frps/frpc同样独立。`internal/platform.Manager`隔离服务操作，运行时不能硬依赖systemctl。Alpine正式支持controller/node/both及可选frp，运行预编译产物、无Node/Go依赖；musl/静态及外部二进制兼容性、安装/启动/日志/重复安装/升级恢复须在T07逐项真机验证。当前只有开发二进制与公共接口，正式服务与安装器未实现。

独立运行的收益主要是故障隔离和维护便利。当前没有资源实测，不承诺固定内存占用，也不以进程数量直接推断总资源消耗。

### 3. 部署、端口与本地文件

#### 安装角色

交付两个自研二进制 `meshstrata-controller`、`meshstrata-agent`；节点发布包另包含经过校验的固定版本 Mihomo。安装脚本根据角色下载对应架构的发布包，校验、安装并完成基础配置，执行结束后退出。

| 安装角色 | 程序与行为 |
| --- | --- |
| `controller` | 仅安装并启动 Controller。 |
| `node` | 安装 Agent + Mihomo，使用提供的 Controller 地址、信任资料与一次性 token 注册。 |
| `both` | 在同机安装 Controller + Agent + Mihomo，并自动完成本机节点注册。 |

`both` 复用常规入网与 mTLS 流程：首次安装先初始化 Controller，再准备可信入网资料并启动本机 Agent，不绕过证书和身份验证。控制连接地址与代理入口地址分开配置；重复安装复用已有状态和节点身份，不重置 CA、不重复注册。各服务独立运行，安装失败需保留并报告可重试状态。

#### 端口与目录

| 项目 | 默认约定 |
| --- | --- |
| Web 后台与订阅 | Controller TCP `8443`，普通 HTTPS，可修改。 |
| 入网与控制通道 | Controller TCP `9443`；入网使用已验证的服务端 TLS 与一次性 token，后续管理使用 mTLS。 |
| Mihomo 管理 API | 默认 Unix socket `/run/meshstrata/mihomo.sock`，不占用 TCP 端口；仅供本机 Agent 使用。显式启用 TCP 兼容模式时绑定回环地址、自定义端口并使用随机 secret。 |
| 代理端口 | 逐节点、逐协议配置，初始自动端口池 `20000–29999`。 |
| Controller 状态目录 | 建议 `/var/lib/meshstrata/controller/`，保存数据库和必要状态。 |
| Agent 状态目录 | 建议 `/var/lib/meshstrata/agent/`，保存节点身份、授权状态及任务检查点。 |
| Mihomo 工作目录 | 建议 `/var/lib/meshstrata/mihomo/`，保存启动配置、候选配置及上一可用版本。 |

以上路径是实现约定，安装器需支持明确配置。Mihomo 文件路径应位于其允许的工作目录内。包含密钥和凭据的目录与文件限制访问权限；日志不得记录 token、secret、密码或私钥。

默认不占用公网 `80/443`，不要求额外安装反向代理；已有 HTTPS 反向代理可以继续使用。安装前分别检查 TCP/UDP、地址及 IPv4/IPv6 监听冲突，不覆盖已有服务。

构建在开发环境或 CI 完成，提供对应 Linux 架构的发布包；VPS 只负责下载和运行。安装器验证发布产物来源与完整性，并保存实际 Agent、Mihomo 版本及摘要。

#### 与节点已有 Mihomo 共存

平台只管理自己安装的内核实例。使用独立可执行文件路径（建议 `/usr/local/libexec/meshstrata/mihomo`）、工作目录、systemd/OpenRC 服务名和 Unix socket，不复用或修改节点已有的 Mihomo 程序、服务、配置及缓存。停止或升级时按平台服务定位实例，不使用 `pkill mihomo` 等按通用进程名操作的方式。

生成的服务端配置显式设置 `port`、`socks-port`、`mixed-port`、`redir-port` 和 `tproxy-port` 为 `0`，关闭通用 HTTP/SOCKS/mixed/透明代理入站，不默认监听 `7890` 等常见本地代理端口。首版服务端同时禁用 TUN 和自动 iptables 接管；这不改变客户端订阅中按需生成 TUN 的设计。代理业务仅通过平台显式分配的协议 Listener 接入。

内部目标解析仍按节点 `Target DNS` 策略配置，但 `dns.listen` 默认留空，不占用宿主机 DNS 监听端口；禁用 DNS 监听不等于禁用内部域名解析。管理 API 的 TCP/TLS 地址默认留空，仅启用专属 Unix socket。v1.19.33 的 Unix socket API 不验证 secret，且内核会将 socket 文件权限设为 `0666`；必须由安装器及所选init服务 预先建立受限的 `/run/meshstrata/` 父目录（例如 `0700` 并采用匹配的服务身份，或使用受限组的 `0750`），以目录访问权限限制本机连接，不能只依赖 secret 或 umask。

以下是共存约束片段，不是可直接启动的完整配置；DNS 上游、启动解析路径、Listener 和业务规则由生成器补齐：

```yaml
port: 0
socks-port: 0
mixed-port: 0
redir-port: 0
tproxy-port: 0
allow-lan: false
tun:
  enable: false
iptables:
  enable: false
dns:
  enable: true
  listen: ""
external-controller: ""
external-controller-tls: ""
external-controller-unix: /run/meshstrata/mihomo.sock
```

安装和每次启动前必须先生成、检查平台完整配置，再以显式的 `-d`、`-f` 路径启动；配置缺失时停止并报告，不能让内核自行生成默认配置。v1.19.33 的初始化逻辑会在缺失配置时生成 `mixed-port: 7890`，因此只在正常配置里禁用端口还不够。

端口冲突检查覆盖平台业务 Listener、按需探测端点和显式启用的管理 TCP 端口，按地址、TCP/UDP、IPv4/IPv6 通配绑定判断。固定端口冲突返回 `PORT_IN_USE`；自动分配只能在允许的端口池中选择并记录实际端口，不能停止已有服务或擅自迁移正在使用的 Listener。已有实例的 TUN/透明代理若捕获平台出站，也需按需验证并提示管理员调整，不能宣称仅避免端口冲突即可保证全部网络行为独立。

### 4. 配置发布与热重载

日常新增线路、调整规则和撤销身份通过 **配置重载** 完成，不把重启整个 Mihomo 进程作为常规发布方式。修改 YAML 文件后必须主动通知内核加载。

Agent 默认通过专属 Unix socket 上的 HTTP API 调用 `PUT /configs?force=true`，传入候选配置的绝对路径；显式启用 TCP 兼容模式时附带 Bearer secret。这里的 `force=true` 不代表重启进程。该接口用于应用代理、规则、DNS 和监听配置；管理 API 自身地址、认证等启动设置的变更需要另行设计，不能假定同一重载接口会同步更新它们。

发布流程：

1. Controller 持久化发布任务、目标节点、期望版本及配置摘要；同一节点的发布、撤销和到期操作串行协调。
2. Agent 接收任务并幂等检查，合并本机最新的到期和撤销状态，生成候选配置，保留上一可用版本。
3. 使用固定版本 Mihomo 校验候选配置的语法、依赖和端口计划。校验成功不等于实际监听成功。
4. 多跳先应用后续节点，再应用入口节点；通过本机 API 加载候选配置。
5. 核对实际监听地址、TCP/UDP 和端口，执行最小协议认证与少量转发检查；按实际协议能力检查 UDP。
6. 检查通过后，将确认配置原子更新为下次启动使用的持久配置，记录摘要、检查结果及时间，回报 `Applied & Verified`。
7. Controller 在收到各节点确认并完成线路端到端检查后确认发布；失败时停止推进并回滚受影响节点，逐一确认回滚结果。

候选、已应用、已验证和启动配置状态需要本地持久记录。Agent 或 Mihomo 在发布中途退出后，应基于检查点重新核对运行状态，不根据一次 API 返回值直接认定成功。

热重载的影响边界：

- v1.19.33 会比较命名 Listener 配置；未变化的 Listener 可复用，发生变化的 Listener 会关闭并重建。
- 不承诺热重载对全部现有连接无影响，也不承诺已有连接立即采用新的路由或 DNS 策略。
- API 返回成功不代表全部 Listener 已成功监听；运行检查是发布成功的必要条件。
- 内核程序升级通常需要重启进程，应与普通配置重载区分。
- 回滚后也要检查运行状态；无法确认时显示 `Rollback Pending` 或 `Rollback Failed`，不能显示成功。

### 5. 本地到期与独立运行的约束

Mihomo 不承担平台订阅有效期调度。Agent 在本地持久保存授权、到期时间和已知撤销状态，通过本地定时器执行，Controller 失联时仍能工作。

- Agent 启动或恢复后，先处理已过期和已知撤销身份，再执行其他配置操作。
- Mihomo 每次启动前，通过本地预处理步骤过滤启动配置中的失效身份；该步骤使用本机状态，不依赖 Controller 在线。普通 Agent 重启不触发 Mihomo 重启。
- 发布、回滚和重连都必须重新合并本机失效状态，不能通过恢复旧版本重新授权。
- SS 2022 和 Hysteria 2 的独立订阅入口按产品设计隔离 Listener、端口与凭据；其他入口只有验证能够独立撤销且不影响其他条目后才允许共享。
- 撤销后验证旧凭据无法建立新连接，并处理能够准确归属的已有连接；共享资源回收需要独立确认。

Agent 退出期间，到期调度会暂停；systemd/OpenRC 自动恢复和启动校正用于缩短并处理该窗口。不能宣称 Controller 离线容错等同于 Agent 永久故障时仍保证准时撤销，此边界需纳入故障测试。

### 6. 数据与资源控制

- Controller 使用本机 SQLite，启用 WAL，采用短事务并控制写入并发；不通过网络文件系统共享数据库。备份使用一致性备份方式，并包含恢复所需密钥，不能只复制运行中的主数据库文件。
- 数据库保存业务模型和修订关系，Mihomo YAML 是生成物，不作为唯一业务数据来源。Listener 引用变更及端口释放需幂等处理，收到节点确认后才能回收资源。
- Agent 本地只保存必要状态、配置版本和轻量计数检查点；启动、到期与重载操作应共用一致的状态更新机制。
- 资源、网络、RTT 和用量按需查询，不持续扫描全网或同步资源指标；只保留必要心跳、到期执行、证书维护和日志清理。
- 探测与发布限制并发并设置超时；页面关闭后停止本次页面触发的刷新任务。缓存过期本身不触发全网自动探测。
- 日志沿用产品默认值：warning/error，单文件 5 MiB，最多 4 份（含当前），总量不超过 20 MiB，最长 7 天；systemd journald与OpenRC监督器日志均需另设相应限额。
- 内存、空闲 CPU、磁盘增长、连接规模和发布期间峰值在原型完成后实测，再形成部署规格。

### 7. 首轮开发顺序

1. 建立 Go Controller/Agent 与 Vue 前端工程，完成静态资源嵌入、SQLite 迁移和英文 i18n 基础。
2. 完成节点入网、mTLS WebSocket、断线重连及任务幂等机制。
3. 打通独立 Mihomo 安装、配置校验、API 重载、运行确认和失败回滚。
4. 验证四种协议的 Listener 行为、配置重载影响、身份撤销、到期与进程恢复，再确定首个交付内核版本。
5. 实现线路、订阅、引用回收、规则与 DNS 生成，然后接入按需推荐和出口检查。

位置服务首接IPinfo，自动结果不覆盖手工位置。完整分批顺序、独占目录、迁移槽位与测试关口以[剩余开发流程](Meshstrata_剩余开发流程_v1.0.md)为准。基础工程已完成，具体实现和验证范围见下方日志；其余步骤仍为后续计划。原型优先证明单节点发布与撤销闭环，再扩展多跳。

### 8. 实现参考

- [Go embed](https://pkg.go.dev/embed)、[Vite 生产构建](https://vite.dev/guide/build.html)、[Vue I18n](https://vue-i18n.intlify.dev/)。
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)、[SQLite WAL](https://www.sqlite.org/wal.html)、[sqlc](https://docs.sqlc.dev/en/latest/)。
- [Go TLS](https://pkg.go.dev/crypto/tls)、[lego](https://github.com/go-acme/lego)。
- [Mihomo API](https://wiki.metacubex.one/api/)。
- Mihomo v1.19.33：[配置重载接口](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/route/configs.go)、[配置执行](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/executor/executor.go)、[Listener 更新](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/listener/listener.go)。
- Mihomo v1.19.33：[缺失配置时的初始化](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/config/initial.go)、[Unix socket API 及权限](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/route/server.go)、[DNS 监听](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/dns/server.go)。

## 日志记录

后续按日期追加记录，分别说明变更、验证结果、已知问题和下一步；已确认方案发生变化时，同步更新开头的设计并在当天记录原因。

### 2026-11-09｜确定技术栈与内核部署方式

- **决策：**采用 A 方案，即 Go + Vue 3 + TypeScript + Vite + SQLite；Mihomo 外挂独立运行，安装器统一安装，Agent 通过本机 API 管理。
- **原因：**优先减少外部常驻服务与部署步骤；保留 Agent 和代理内核的独立升级、恢复能力，不为未经实测的内嵌资源收益增加维护耦合。
- **文档变更：**创建本开发日志，在开头记录技术栈、部署边界、配置热重载、发布校验、到期处理及首轮开发计划。
- **验证范围：**对照产品设计 v0.11 和本次已核对的 Mihomo v1.19.33 接口与源码整理设计；当前没有实现代码、构建结果或资源基准数据。
- **下一步：**搭建最小工程，并验证单节点配置发布、实际监听确认、身份撤销与失败恢复闭环。

### 2026-11-09｜补充与节点已有 Mihomo 共存约束

- **决策：**平台内核关闭通用代理入站、TUN 和自动透明代理接管，不默认监听 `7890` 等端口；管理 API 改为独立 Unix socket，DNS 保留内部解析但不监听宿主机端口。
- **实例隔离：**程序、配置、状态、服务和 socket 使用平台专属位置；安装、升级和停止操作不修改节点已有 Mihomo 实例。
- **启动约束：**先准备完整配置，缺失则拒绝启动，避免内核初始化生成默认 `mixed-port: 7890`；启动和重载均检查实际监听。
- **文档变更：**同步产品设计至 v0.12，更新本日志的默认管理接口和配置发布描述。
- **验证范围：**已核对 v1.19.33 的初始化、Unix socket API 和 DNS 监听源码；尚未实现安装器，也未执行双实例共存测试。Unix socket 的目录权限隔离、已有 TUN/透明代理影响和异常启动路径需纳入后续验收。

### 2026-11-09｜安装并验证本机开发环境

- **安装：**在 Ubuntu 26.04.1 LTS / amd64 安装 Go 1.27.3、Node.js 26.13.0 LTS（npm 11.20.0）、sqlc 1.31.1 和 SQLite CLI 3.46.1；四个上游发布二进制包（含下述 Mihomo）安装前均通过官方 SHA-256 校验。
- **位置与使用：**Go、Node.js 和 sqlc 位于 `/opt/meshstrata-dev/`，命令链接已加入 `/usr/local/bin`，无需额外激活；SQLite CLI 位于 `/usr/bin/sqlite3`。下载地址与摘要保存在 `/opt/meshstrata-dev/install-manifest.json`。本机约 1.7 GiB 内存，Go 首次构建可用 `GOMAXPROCS=2 go build -p 2 ./...` 限制并发。
- **Mihomo 隔离：**项目测试内核固定 v1.19.33，安装到 `/usr/local/libexec/meshstrata/mihomo-v1.19.33`，通过 `meshstrata-mihomo` 调用。保留宿主机已有 `/usr/local/bin/mihomo`、运行进程和监听，不安装项目常驻服务。
- **前端验证：**临时工程使用 Vue 3.5.45、TypeScript 6.0.3、Vite 8.3.6、Naive UI 2.45.3 和 Vue I18n 11.4.18，类型检查及生产构建通过。
- **后端验证：**sqlc 生成 SQLite 访问代码；使用 `modernc.org/sqlite` 1.62.0，在 `CGO_ENABLED=0` 下构建通过，数据库插入/查询和 Go 嵌入前端的 HTTP 响应检查通过；SQLite CLI 完整性检查通过。
- **内核验证：**使用独立工作目录和完整配置执行 Mihomo `-t` 校验通过，未启动新的监听。此检查不覆盖四协议转发、配置热重载或生产部署验收。
- **软件源处理：**原系统 HTTP 软件源更新无响应，本次临时指定 Ubuntu 官方 HTTPS 源安装 SQLite，未修改原软件源文件。
- **范围：**环境验证工程位于 `/tmp/meshstrata-dev-check`，不属于项目源码；前端及 Go 库由后续工程的依赖文件管理。开发环境信息统一记录于本节，不另设说明文件。
- **下一步：**按已确认顺序建立最小 Controller、Agent 和前端工程，在工程依赖与锁文件中固定版本。

### 2026-11-09｜确定安装角色与 Controller 同机节点方案

- **决策：**Controller 与 Agent 保持独立进程，首版不内置常驻 Agent 功能；支持 `controller`、`node`、`both` 三种安装角色。
- **同机部署：**`both` 一次安装 Controller、Agent 与独立 Mihomo，并自动按常规可信入网和 mTLS 流程注册本机节点；后续沿用统一发布、校验、撤销与回滚机制。
- **运行边界：**普通 Controller 重启不暂停本机 Agent 的到期处理，也不联动停止 Mihomo；重复安装复用已有身份及状态。
- **文档变更：**产品设计更新至 v0.13，增加安装角色、同机部署及验收要求；同步更新本日志开头的方案。
- **验证范围：**本轮为文档修改，核对组件职责、mTLS、服务独立性和安装角色描述的一致性；安装器与服务故障场景尚未实现验证。

### 2026-11-09｜搭建 Controller、Agent 与内嵌前端基础工程

- **工程：**建立单一 Go module，提供独立 `meshstrata-controller`、`meshstrata-agent` 入口；共用 JSON 配置加载、英文结构化日志、构建信息和命令处理。默认日志级别为 `warn`，开发示例使用 `info`；支持配置校验、版本输出与 SIGINT/SIGTERM 优雅退出。
- **Controller：**开发环境仅监听回环 HTTP，默认 `127.0.0.1:8443`；提供 `/healthz`、`/readyz` 和 `/api/v1/system`，未知 API 使用稳定 JSON 错误码。内嵌前端缺失时明确拒绝启动，不创建伪成功页面。
- **存储：**使用固定版本 `modernc.org/sqlite`、显式 SQL 与 sqlc；启动执行带历史记录的事务迁移，启用 WAL、外键、5 秒 busy timeout 和单连接。初始只保存实例元数据，重复启动保留实例 ID；拒绝较新或不完整的迁移历史。
- **Agent：**不依赖 Controller 或 Mihomo 即可独立运行，仅通过私有状态目录下的 Unix socket 提供本地健康检查，并提供 `healthcheck` 命令。进程锁阻止重复启动，恢复崩溃遗留 socket，不覆盖同名普通文件，正常退出清理 socket。健康状态不代表节点已入网或代理可用。
- **前端：**建立 Vue 3、TypeScript、Vite、Naive UI 和英文 Vue I18n 工程，固定已验证的版本并提交 npm 锁文件。提供七项导航、Dashboard 按需系统信息查询及加载/失败/重试状态，其余页面明确标注待实现；不填充模拟业务数据，不自动轮询。生产资源通过 Go `embed` 随 Controller 交付；Agent 不链接 Controller 的前端或 SQLite。
- **开发交付：**增加英文 README、开发配置、Make 构建/检查/代码生成/独立启动命令。状态分别写入 `.local/controller`、`.local/agent`，目录为 `0700`，Agent socket 为 `0600`；依赖、缓存、二进制和生成的前端文件不入库，提交 sqlc 生成代码和嵌入占位文件。
- **验证：**`make check` 全部通过，包含 Go 单元测试、`go vet`、sqlc 生成一致性、前端类型检查与生产构建、`CGO_ENABLED=0` 的两个二进制构建，以及源码目录之外的临时进程冒烟测试。覆盖配置错误、迁移失败回滚、数据库就绪状态、缺失前端、静态资源、实例 ID 持久性、SQLite 完整性、重复启动、独立启停及健康 socket 清理。
- **验证环境：**本机 `/tmp` 为容量有限的 tmpfs，Make 将 Go/npm 缓存和 Go 编译临时文件放到项目 `.local/cache/`，Go 并发默认限制为 2。沙箱禁止 Unix socket 所需的操作，因此完整运行检查在沙箱外使用临时状态目录、回环端口和项目子进程完成；没有启动项目 systemd 服务，也未操作宿主机既有 Mihomo。
- **已知边界：**本轮是本地开发基础，暂不实现公网 HTTPS、登录、管理 CA、入网、mTLS WebSocket、Mihomo 管理、线路/订阅/发布、文件日志轮转、systemd 模板及三角色安装器；产品文档中的生产部署约束继续作为后续目标。
- **下一步：**实现可信节点入网、节点独立证书、mTLS WebSocket、心跳重连及任务幂等，再推进单节点配置发布与撤销闭环。

### 2026-11-09｜补充反向出口并统一产品设计版本为 v1.0

- **设计范围：**反向出口首版仅由 Controller 同机 `both` 节点承接，Agent 管理可选 frp TCP + mTLS，内部使用 SS 2022 TCP，线路固定为“管理机节点 → 远端出口”；补充独立状态、映射授权、发布回滚、重连与撤销验收要求。
- **版本与引用：**产品设计更新为 v1.0，文件更名为 `Meshstrata_产品设计_v1.0.md`；同步 README 和本日志的链接及当前版本，保留文档内的历史版本记录。
- **验证范围：**本轮仅更新文档；检查版本、文件名、引用、Markdown 代码围栏及差异格式，未执行隧道部署或业务转发测试。

### 2026-11-10｜设计前端交互与分阶段实施任务

- **目标：**先实现可交互前端原型，帮助后续接口设计；不接业务 API，不执行真实节点配置、测量、发布或撤销。
- **导航：**按用户授权优化为 Overview、Nodes、Routes、Client Config、Activity，Settings 位于侧栏底部；Subscriptions 与 Rule Profiles 合并到 Client Config 二级入口。出口策略保留在线路上下文侧栏，发布/回滚统一进入任务详情。
- **交互设计：**新增 `Meshstrata_前端交互设计_v1.0.md`，明确页面路由、操作和返回关系、草稿、四步线路向导、出口规则、订阅与规则往返、状态/观测口径、响应式和可访问性要求。
- **实施拆分：**新增 `Meshstrata_前端实施任务_v1.0.md`，为基础设施 F00 与十个业务模块 P01–P10 明确文件归属、依赖、检查和交付规则；按用户允许的节奏先启动 F00，完成后由用户唤醒设计会话安排各业务会话。指定模型 GPT-6.1 Sol，推理强度 High。
- **技术决定：**沿用 Vue/TS/Naive UI/Vue I18n；计划增加 hash 路由、公共布局和组件、共享演示 workspace、独立 feature 语言资源和页面占位入口，不需要为前端路由改 Go 静态服务器。
- **验证范围：**本轮为设计与任务拆分，核对产品边界、路由/页面归属、文档链接及格式；前端实现和运行验收由后续任务完成。

### 2026-11-10｜交付 F00 前端基础设施与页面契约

- **导航与路由：**固定引入 Vue Router 4.6.4，使用 hash history；完成 19 个命名页面入口、根路径重定向、动态对象上下文、面包屑、活跃导航、页面标题、未知路径/对象提示、query 白名单与内部 returnTo 校验。采用 Overview、Nodes、Routes、Client Config（二级 Subscriptions / Rule Profiles）、Activity，Settings 位于侧栏底部；演示登录为独立布局。
- **共享基础：**拆分应用 providers/layout、公共页面/状态/查询时间/错误/影响确认/实体链接/文字路径/校验与任务反馈组件。补齐全部 P01–P10 指定入口和两个出口抽屉的稳定 props/emits；业务页面明确显示 Pending implementation，没有实现业务算法或将占位检查标成成功。
- **演示状态：**新增共享 domain 类型、只读响应式 workspace 与显式写命令，关联的本地 both Relay、HK/SG/JP Direct、Reverse、离线节点、线路/策略/规则/订阅/发布/用量 seeds；独立保存连接、验证、草稿/已发布修订和清理状态。提供 normal/empty/error/stale/offline/partial 可复现场景及重置入口，无随机结果。
- **草稿与请求边界：**版本化保存非敏感演示对象与投影后的草稿，损坏/不可用存储提示重置，敏感输入独立保留内存。提供 Save Draft / Discard / Stay 离开保护、locale 日期/数字、保留旧快照的查询状态、可取消且卸载清理的一次性模拟任务。Settings 的基础设施沙盒用于后续浏览器检查。系统信息改为明确点击后才调用原 getSystem，带超时/中止，离线不阻断原型；没有业务 API 或自动探测请求。
- **契约与文案：**新增 `web/FRONTEND_CONTRACT.md`，记录路由 helper、公共组件、抽屉接口、模型/读写命令、草稿/场景、i18n、文件归属和后续边界；feature 英语资源独立装载，同步更新 README 的离线运行说明。未修改 Go、产品设计、交互设计或业务任务书。
- **验证：**通过 `npm --prefix web run typecheck`、`npm --prefix web run build`、`npm --prefix web run check:foundation`、`git diff --check`。基础逻辑检查覆盖全部命名路由、query 清理、returnTo 拒绝外链、内存路由返回、404、六种 seeds 与引用、持久化往返/损坏、敏感字段排除及重置。源码检查确认 fetch 只在显式 Controller 查询链路出现，无 setInterval；响应式 CSS 在 <=760px 收起侧栏并使用抽屉。
- **验收限制：**宿主未提供可操作的浏览器工具或浏览器可执行文件，未进行浏览器 hash 刷新/前进后退、无效 ID 页面、草稿确认、焦点/窄屏渲染和网络面板实测；内存路由/源码检查不等价于这些交互验收。未重跑 Go 检查，因本轮不改 Go。所有 P01–P10 业务界面和出口检查逻辑仍待实施。
- **后续：**由用户唤醒原设计会话审阅契约与构建，再按任务书启动业务页；本轮没有创建其他会话或发送跨会话消息。领域模型、query 或草稿字段扩展由集成会话协调。

### 2026-11-10｜复核公共契约并准备页面会话

- **基础复核：**确认 F00 会话完成，工作区干净；重新通过 19 个路由和持久化基础检查，以及类型检查与前端生产构建。业务页仍为明确占位。
- **共享字段：**为节点协议/DNS/资源、订阅分组/客户端、规则文本、任务阶段、用量口径与八组设置增加兼容旧快照的可选字段；扩展页面需要的 query，提供基于稳定 ID 的出口组名 helper。
- **草稿与存储：**为节点接入、线路、订阅、规则增加严格字段投影的非敏感表单数据，保存/恢复均过滤额外字段。修复持久化校验错误地要求网卡目标含 host/port、订阅撤销任务含 routeId 的问题，条目必填 routeId 仍受校验。
- **演示一致性：**普通节点准备四种协议默认值，SS 订阅条目分配独立 Listener/端口，混合线路组改为 All-Proxies，系统端口池对齐 20000–29999。已有浏览器演示数据不会被自动覆盖，可通过 Reset Demo 加载新 seeds。
- **会话协调：**P01–P10 采用独占 feature 目录并行编辑，检查经新增 `web/scripts/check-pages.sh` 的 flock 锁串行执行；禁止页面会话操作共享 Git index，统一由设计/集成会话提交。扩展约定已写入 `web/FRONTEND_CONTRACT.md`。
- **验证：**新增合法目标/任务变体、缺失必填引用和完整表单草稿恢复/字段过滤的回归检查；`check:foundation`、`typecheck`、生产构建和差异格式检查通过。浏览器交互实测仍待页面集成后完成。

### 2026-11-10｜集成 P01–P10 前端页面与操作原型

- **交付：**十个 GPT-6.1 Sol / High 页面会话完成；统一集成总览、节点/接入/详情、线路/四步向导、出口抽屉、订阅与配置、规则与 DomainSet、活动/发布任务、设置与演示登录。19 个命名入口与对象跳转均有实际内容；不调用业务接口。
- **交互优化：**保留面向任务的主导航、底部 Settings、对象关联跳转、显式草稿、发布影响审阅、订阅到规则编辑的返回关系。补齐独立出口草稿与白名单，统一当前 session 配对语义，保留失败编辑前的已发布版本；修正固定端口冲突、自动分配范围、Node Default 分组、重复发布草稿、DomainSet/DNS 输出和续期分配。
- **集成验证：**`bash web/scripts/check-pages.sh` 通过，包括基础路由/持久化/草稿/配对、跨页面配置逻辑、活动任务回归、vue-tsc 和 Vite 生产构建；`git diff --check` 通过。
- **浏览器验证：**隔离 Playwright 1.56.1 / Chromium 141 在生产静态产物上通过 69 项逐文档检查：全部入口、无效 ID、六种场景和 390px 窄屏；运行时错误、自动业务请求、全页横向溢出和控制台警告均为 0。实际点击验证线路候选→出口草稿保存/刷新→Node Default→模拟发布→任务详情，以及订阅→新建 Profile→返回→资料保留→生成订阅。修复验收发现的详情/规则页网格溢出及输入标签关联。
- **工具与产物：**增加可复用纯逻辑和可选浏览器验收脚本。浏览器、依赖库、字体及截图均在忽略的 `.local/browser-check`，未安装系统软件或改变应用依赖；临时预览仅绑定回环并在验收后停止。README、共享契约、会话登记与 `frontend-delivery/集成验收.md` 已更新。
- **边界：**全部动作是有限离线模拟，导出仅为脱敏示例；真实 API、登录、节点控制、运行时协议验证和配置部署留给后续实现。Activity 重试仅推进任务记录，共享策略批量更新和未验证 IPv6/业务 UDP 明确不可用。节点协议/probe/无引用 Listener 的未保存输入只保留当前页。未改 Go，未重跑后端部署/业务转发测试。完整范围与限制见集成验收记录。

### 2026-11-11｜添加节点改为一键命令接入交互

- **目标：**用户实时预览前端，先把添加节点调整为一键安装命令、复制到宿主机、等待节点上线的流程，继续保持无业务接口的原型范围。
- **实现：**在 `controller/web/src/features/nodes` 提供命令预览生成器，整合 Controller/信任资料/一次性 token/节点模式与地址或 Relay；安装详情折叠，复制后自动进入逐项等待，支持手动复制、失败重试、停止等待、过期重新生成与完成后的节点设置入口。
- **状态：**token 只保留在内存，生成命令/基本资料变更会失效旧状态；刷新不会恢复 token，运行中检查十五分钟过期；成功后消费命令，进入设置不再依赖其有效期。命令整体注释，明确是未来 CLI 示例，不能执行真实安装或连接。
- **验证：**完整前端基础/跨页逻辑/活动逻辑检查、类型检查及生产构建通过；新增专用 Playwright 接入流程回归覆盖 Direct/Reverse、复制与等待、失败重试、取消、草稿刷新、过期、剪贴板降级、Shell 转义与 390px 窄屏，无页面运行错误或业务 API 请求。截图及日志在忽略的 `.local/browser-check/enrollment`。
- **工作区：**复用用户正在查看的 5173 开发服务并保留热更新。开始本轮时仓库已有大范围未提交的 Controller/Agent 目录迁移；本轮没有暂存、撤销或混合提交这些既有改动，前端改动随当前工作区保留，后续应与目录迁移完成后的状态分开审阅并提交。

### 2026-11-11｜按网络监管级别统一推荐偏好

- **用户修正：**创建线路不应要求反复选择含糊的 Access environment，分类改为强监管/弱监管，不再以 Mainland/Outside 代表实际网络条件。
- **界面：**Create Route 第一步改为 Exit node，默认继承 Settings 的客户端监管级别与 UDP 偏好，在默认折叠的 Recommendation preferences 中允许仅覆盖当前草稿。说明其作用为新增首跳协议建议与测量样本分组，区别于出口位置和节点标签，不重写选定 Listener 或已发布路径。Settings、节点新建/编辑/筛选、线路测量和摘要统一使用 Strong regulation / Weak regulation。
- **兼容：**规范值为 `strong_regulation` / `weak_regulation`；读取旧快照时兼容转换地区枚举，涵盖节点、Settings、发布/草稿修订、嵌套线路草稿和测量记录，并兼容旧节点筛选链接。不会根据节点国家/IP 自动赋值，不重置演示数据。统一缺省为弱监管，修正创建与 Settings 原先不同的默认回退值。
- **验证：**`bash controller/web/scripts/check-pages.sh` 通过基础回归、跨页逻辑、活动逻辑、类型检查和生产构建；新增覆盖旧数据转换、首跳协议建议与缓存分组隔离。专用 `check-regulation-browser.cjs` 通过默认继承、折叠偏好、草稿覆盖、协议建议、统一分类、旧草稿/链接与 390px 窄屏检查，无运行错误、翻译缺项或业务请求。预览/结果在忽略的 `.local/browser-check/regulation`，开发服务继续保留。
- **交付状态：**产品设计与交互设计已同步。当前仍有本轮开始前的大范围目录迁移未提交，本轮未夹带或撤销该重构；变更保留在当前工作区，待目录迁移完成后分别审阅并提交。

### 2026-11-11｜交付简体中文与英文界面适配

- **执行方式：**按用户指定启动 GPT-6.1 Sol / Extra High 执行子会话。独立侧栏新会话工具连接失败未创建成功，执行子会话完成主实现后因工作区额度不足中断；用户要求继续后，由原设计会话完成剩余修复与验收。
- **界面：**新增通用与全部 11 个 feature 的 zh-CN 资源；Settings 提供 English / 简体中文即时切换和保存，首次跟随浏览器，保存选择优先，中文变体归一为 zh-CN，不支持的值回退英文。Reset Demo 保留明确语言选择。Naive UI 文案与日期、Intl 格式、HTML lang 和当前页面标题同步变化，无须再次导航。
- **数据与边界：**语言切换不重写对象名、事件、规则语法、机器标识、协议参数或用户输入；强/弱监管分类及节点命令接入流程保留。补齐订阅日期数字、观测展示枚举和动态状态/校验文案。仍为离线原型，不新增业务 API 或真实接入。
- **收尾修复：**损坏的语言偏好即使是非字符串也不阻止工作区启动，不丢弃其他对象；域名集编辑期间被移除时，中文/英文错误正确传递 detail 标识；验收脚本兼容 Not Found 的 h2 标题，并在成功后清理历史失败截图/记录。
- **验证：**完整 check-pages 通过，包含基础检查、12 模块 1,990 个文案键/插值/双语编译、1,770 静态调用、166 动态校验发出点、状态矩阵、跨页/活动逻辑、类型检查及生产构建。真实浏览器通过 118 次双语/页签/场景/窄屏访问和 7 组行为检查，运行错误/翻译警告/业务请求/横向溢出均为 0；额外通过损坏偏好、记录保留与域名集错误插值回归。git diff --check 通过。详见 frontend-delivery/P11-i18n.md。
- **运行与工作区：**恢复并保留 127.0.0.1:5173 开发服务；测试依赖、中文字体、日志和截图放在忽略的 .local/browser-check。已有未提交的 Controller/Agent 目录迁移及前轮前端改动均保留，没有混合提交或推送，也未运行无关后端部署检查。


### 2026-11-12｜B00公共契约与数据基础

- **公共代码：**新增`internal/domain`稳定随机ID/JSON安全修订、错误/字段定位/冲突、列表和任务/逐节点结果，`internal/control`版本化command/result/receipt/hello及严格解码、ArtifactSource和Agent本地检查点/授权/outbox边界；`internal/platform.Manager`显式systemd/OpenRC。未引入持续遥测。
- **API与前端：**`controller/api`提供OpenAPI/控制JSON Schema及安全域/任务/资源不变量契约；只有原system元数据API已实现，预留命名空间统一404，不开放未鉴权业务写接口。新增独立wire类型和显式live/demo数据接口、字段投影/中止/CSRF/错误处理，未被页面导入，没有切换live或新增自动请求，保留完整演示行为与双语。
- **数据实现：**保持001迁移不变，新增002含37张关系表，覆盖节点、节点DNS、Listener/端口/引用、固定路径与出口修订、规则/DomainSet、订阅条目/身份代次/凭据引用、任务/DAG/逐节点证据、测量/事件/设置。任务意图/DAG落库及结果sequence去重、修订CAS为实际事务代码；引用释放幂等、确认清理才释放端口有SQL约束。稳定entry与可更换identity代次分开，旧撤销墓碑不复活。sqlc查询分文件并冻结共享生成权。
- **完整开发计划：**新增剩余开发流程与backend-delivery登记/B00证据，按固定B00–B18/T01–T08安排独占目录、接口依赖、前端操作/API/持久模型/测试映射、003–020迁移槽位、独立集成与多机验收、返修后测新提交。后续未自动派发，本轮不建worktree/子代理/其他会话，不跨会话发消息。
- **正式平台范围：**同步当前产品设计、README和本日志，Ubuntu/systemd与Alpine/OpenRC并列支持controller/node/both及可选frps/frpc，必须验安装/启动/独立生命周期/日志/重复安装/升级恢复；Alpine运行预编译产物，无Node/Go运行依赖，musl/静态与外部二进制兼容进T07正式验收。首接Cloudflare DNS-01+证书导入、IPinfo+手工覆盖。当前仅契约，B17服务/安装器尚未实现。
- **验证：**完整`make check GO_PARALLEL=1 GOMAXPROCS=1`、`check-pages.sh`（含1990双语键/插值、跨页/活动/适配器检查、类型检查与构建）、生成一致性及差异格式检查通过。新增测试涵盖新库/001升级/instance_id、外键/隔离/凭据代次/墓碑/确认清理、任务幂等/DAG/事务回滚/修订冲突、消息序列化/拒绝及Schema字段一致性。最初沙箱内Agent Unix socket测试被系统权限拦截，随后获准在沙箱外执行原完整检查通过，未跳过或削弱检查。两二进制CGO=0且file确认静态链接。
- **浏览器：**独立临时回环端口使用.local内现有Playwright/Chromium，通过69项页面/场景/390px检查、线路/订阅点击流程，以及118次双语访问和7组行为检查；无运行错误/翻译警告/自动API请求/横向溢出，临时预览已停止，不改原5173服务。日志和截图在忽略的`.local/b00-*`与`.local/browser-check/b00/`。
- **实际边界：**没有远程部署或读取测试服务器凭据，没有操作宿主既有Mihomo；真实管理认证/加密、入网/mTLS、Mihomo控制/发布/到期、业务API、frp/公网证书/位置/测量/安装升级均留后续。存储和接口不能当作真实多机成功。B00独占Git，检查后提交推送当前分支，实际结果见本会话最终回复；阶段1需用户在主协调会话说“继续”才派发。

### 2026-11-12｜I00独立契约复核与阶段1稳定基线

- **产品纠正：**恢复HTTPS `/subscriptions/v1/config/{token}` 专用download-only能力链接，Copy URL可直接导入，header仅可选；刷新后通过管理员+Origin/CSRF保护的专用动作再次获取当前链接。普通DTO不含token/URL。B08/010明确增加subscription_download用途及可恢复密文引用，hash验证、原子重置/撤销，001/002不改。统一no-store/no-referrer/无重定向与能力链接脱敏要求，B00历史报告保留并在I00说明纠正。
- **阶段1接口：**新增internal/secretstore.Cipher/Binding/Sealed、internal/adminauth.Guard、storage.Database作用域内sqlc查询/单库短事务；B01实现、B02注入，9443入网/mTLS独立于cookie。阶段1交接写清构造/错误/独占路径/最低测试及B03→B05边界，没有实现业务。
- **并行验证：**scripts/backend_check.py从明确I00提交建立忽略的单模块白名单副本，仅在副本应用本模块SQL/精确依赖并生成/测试，flock串行、共享缓存及-p1；新增4项隔离/拒绝测试。I01在业务会话结束后统一正式迁移003–005/锁依赖/sqlc/接线及全量检查。
- **验证：**原工作区针对性Go测试、能力URL/刷新恢复/鉴权与404契约、仓储事务提交/回滚/取消测试及helper隔离测试通过。沙箱内既有Unix socket测试遇setsockopt限制，获准沙箱外执行原make check退出0；完整check-pages含i18n/适配器/类型检查/构建退出0；未重复无关浏览器套件。报告及日志路径见backend-delivery/I00.md。
- **交付范围：**实际会话ID由环境一致确认并登记，未创建其他会话/子代理或跨会话发消息，无远程部署/凭据读取/live切换/新业务接口。I00独占Git按约提交推送；B01/B02/B03技术基线已备齐，但须用户在主协调会话说“继续”才派发，本会话完成I00后停止。

### 2026-11-12 I01 阶段1统一集成

- 合入B01管理认证/加密、B02 CA/入网/mTLS与B03固定Mihomo适配器；003–005正式迁移、精确依赖和sqlc生成。001/002及模块历史报告不改。
- Controller显式HTTPS安全模式接真实Cipher/Guard/单一Store，独立9443；本地prepare-local/initialize-admin与Agent enroll可执行，不经argv/普通JSON保存敏感值。开发回环HTTP继续只健康/演示。
- Agent以持久初始状态control_only接真实身份、续期、Hello/保活/重连，命令和receipt拒绝，不冒充B05持久业务执行。移除管理身份不等同代理清理，revoke任务保持待清理。
- 补普通二进制本地组合脚本、当前集成源码真实四协议runner、无Go/Node真机产物构建入口及T01计划。完整检查/证据及限制见backend-delivery/I01.md；T01不自动启动，完整发布/到期T02、Reverse T05、两平台三角色安装/frp T07。


### 2026-11-13｜T01 阶段1独立Ubuntu/Alpine真机验收

- 固定待验产品f5702078aa48f946fb3c9ee35af65e991a4fc493；只新增T01驱动/脱敏报告，无产品实现、迁移或前端修改，无子代理/新会话/跨会话消息。低并发共享锁构建CGO0产物，目标无Go/Node安装，两端传输及最终SHA校验通过。
- Ubuntu24.04/systemd与Alpine3.23.7/OpenRC均实际运行Controller+Agent：受信HTTPS管理员认证/CSRF、入网丢响应同CSR恢复/双CSR竞争、mTLS续期/重连/身份保持、真实移除/旧控制连接消失/旧证书拒绝、独立生命周期、五类故障拒绝及跨UID Unix socket保护通过；control_only没有业务发布执行。
- 固定Mihomo v1.19.33两平台四协议本机TCP/业务UDP/撤销/精确旧会话清理/另一身份隔离/API假成功检查通过。真实双向公网四协议均无回显且TCP超时，不能把坏凭据超时计为认证拒绝。互换Controller角色的远端Agent入网均退出1。
- 正式Controller前后回环ready的窗口内，构建机及另一测试机到两端公网8443/9443均超时；未修改防火墙/云规则/SSH配置，尚未定位上游原因。T01整体阻塞，阶段2不可开始；没有以本机结果替代多机成功。首次驱动错误及超时遗留测试PID均有记录/精确回收。
- Alpine435MiB上真实认证KDF/节点控制有界观测无OOM；最后Controller VmHWM148228KiB、Agent9116KiB，不承诺固定资源。最终两机本轮程序/活动Unix socket均0、原宿主监听一致、0700根保留私有失败证据，关闭本轮SSH复用。
- 交付documents/backend-delivery/T01.md、T01-evidence.json与scripts/integration/t01；检查后独占Git提交推送，实际提交号见最终回复。阶段分工维持T02/T05/T07，前端仍demo。本会话停止，后续由主协调在用户“继续”后安排。

### 2026-11-13｜T01-N01 公网阻塞定点调查

- 实际会话01a1d3da-def4-7640-bdef-25d87decd6d8；沿用checkout，固定待验产品仍f5702078aa48f946fb3c9ee35af65e991a4fc493。只增测试/报告，无业务源码、迁移、前端修改，无子代理/其他会话/阶段2。
- 严格核对两机清单、身份、接口/公网NAT、IPv4源路由、监听所有者及真实存活；独立CGO0随机回显helper在共享锁下低并发构建，单独记录摘要。双端包头证实SYN/UDP出站，目标无外部入站；单端口取消源过滤并以回环阳性对照再次确认，内核抓包丢弃0。
- Ubuntu本机INPUT ACCEPT；Alpine获授权安装小型诊断CLI后确认运行nft为空、iptables ACCEPT、防火墙服务未启动；未加载包内示例规则。未修改网络/SSH/云规则；尚不能区分具体云ACL、安全组或Alpine公网NAT映射。没有以超时计认证拒绝或以随机回显计代理业务成功。
- 本轮44次回环字节相等，88次公网探针超时，真实跨机控制/CSR竞争及双向四协议补验仍阻塞。T01原结论不覆盖，阶段2门槛仍否；给出含精确/32及45分钟规则/回滚的私有恢复操作单，需云侧定位/最小恢复后再补验。
- 两轮各自精确PID/tcpdump/Unix socket清理、原监听一致、固定产物最终SHA校验均通过；0700失败证据保留，SSH复用关闭，无公共代理。Go构建/vet、Python语法及diff检查通过，不跑无关前端全套。交付backend-delivery/T01-N01.md与对应JSON，独占Git提交推送后停止。
