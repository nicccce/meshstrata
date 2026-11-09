# Meshstrata｜开发日志

**开发约定：每一轮代码、配置或文档修改完成并通过相应检查后，都必须创建 Git 提交，并推送到当前工作分支对应的远端分支。推送失败时保留本地提交，明确说明失败原因及当前状态；确认推送成功后才能报告已完成推送。**

## 已确认方案：A 方案 + 独立 Mihomo

**确认日期：2026-11-09（Asia/Shanghai）｜状态：设计已确认，尚未实现**

本项目采用 **Go + Vue 3 + TypeScript + Vite + SQLite**。Mihomo 作为随安装器交付的独立程序运行，由 Agent 通过本机管理 API 管理。设计目标是轻量、低常驻资源、易部署，并使 Agent 的更新与代理转发进程相互独立。

产品需求以 [Meshstrata_产品设计_v0.9.md](Meshstrata_产品设计_v0.9.md) 为依据；该文件正文当前版本为 **v0.12｜2026-11-09**。开发日志记录技术决策、实现进展和验证结果，不替代产品验收要求。

### 1. 技术栈

| 层面 | 选型 | 用途与约束 |
| --- | --- | --- |
| Controller | Go，HTTP 优先使用标准库 `net/http` | 单进程提供 Web 静态资源、业务 API、订阅下载、发布调度和节点控制通道。 |
| Agent | Go | 管理本机内核配置、按需探测、运行校验、凭据到期、证书维护和日志清理。 |
| 前端 | Vue 3 + TypeScript + Vite | 构建静态 SPA，通过 Go `embed` 随 Controller 发布；生产服务器无需运行 Node.js。 |
| 界面组件 | Naive UI，按需使用 | 实现表格、协议表单、线路向导、侧栏和发布状态。 |
| 国际化 | Vue I18n | 首发英文资源，预留中文；组件使用资源键，稳定 ID、配置字段和代理组标识不翻译。 |
| Controller 数据库 | SQLite + `modernc.org/sqlite` | 数据保存在本机持久目录，不引入独立数据库服务；驱动不依赖 CGO，便于构建发布包。 |
| 数据访问 | 显式 SQL + sqlc | 管理配置、依赖、订阅身份、测量缓存和发布任务；使用事务维护引用与状态一致性。 |
| 节点控制通道 | WebSocket + JSON over mTLS | Agent 主动连接 Controller，传递任务与结果；空闲时只保留必要心跳和重连。 |
| 内部身份 | Go TLS/X.509 + 内部管理 CA | 一次性入网 token、节点独立证书、续期与撤销。 |
| 公网证书 | 成熟 ACME 实现，优先评估 lego | 默认 DNS-01；Controller 管理 DNS API 凭据，节点私钥留在节点。具体 CSR 集成在实现阶段验证。 |
| 代理内核 | 独立 Mihomo，固定版本 | 初始以产品文档核对的 v1.19.33 为兼容性基线；完成验证后才能确定首个交付版本。 |
| 部署 | Linux 二进制 + systemd | 安装器完成文件、服务与端口配置；首版节点使用主机网络。 |

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

- Controller 通常不运行 Mihomo；只有同机还承担代理节点职责时才安装节点组件。
- 每台代理节点运行一个 Agent 和一个 Mihomo 实例；多条线路使用同一实例中的 Listener、Proxy 和规则，不按线路启动内核进程。
- Agent 与 Mihomo 分别使用 `meshstrata-agent.service`、`meshstrata-mihomo.service`。systemd 负责保活；Agent 负责配置、健康确认及显式的内核升级、回滚操作，避免多个组件竞争管理进程。
- 普通 Agent 更新或重启不联动停止 Mihomo；内核升级、内核崩溃或 Listener 重建仍可能影响代理连接。
- 首版采用安装器同时安装两个程序，不把 Mihomo 源码链接进 Agent，也不要求用户手动安装和配置内核。

独立运行的收益主要是故障隔离和维护便利。当前没有资源实测，不承诺固定内存占用，也不以进程数量直接推断总资源消耗。

### 3. 部署、端口与本地文件

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

平台只管理自己安装的内核实例。使用独立可执行文件路径（建议 `/usr/local/libexec/meshstrata/mihomo`）、工作目录、systemd 服务名和 Unix socket，不复用或修改节点已有的 Mihomo 程序、服务、配置及缓存。停止或升级时按平台服务定位实例，不使用 `pkill mihomo` 等按通用进程名操作的方式。

生成的服务端配置显式设置 `port`、`socks-port`、`mixed-port`、`redir-port` 和 `tproxy-port` 为 `0`，关闭通用 HTTP/SOCKS/mixed/透明代理入站，不默认监听 `7890` 等常见本地代理端口。首版服务端同时禁用 TUN 和自动 iptables 接管；这不改变客户端订阅中按需生成 TUN 的设计。代理业务仅通过平台显式分配的协议 Listener 接入。

内部目标解析仍按节点 `Target DNS` 策略配置，但 `dns.listen` 默认留空，不占用宿主机 DNS 监听端口；禁用 DNS 监听不等于禁用内部域名解析。管理 API 的 TCP/TLS 地址默认留空，仅启用专属 Unix socket。v1.19.33 的 Unix socket API 不验证 secret，且内核会将 socket 文件权限设为 `0666`；必须由安装器/systemd 预先建立受限的 `/run/meshstrata/` 父目录（例如 `0700` 并采用匹配的服务身份，或使用受限组的 `0750`），以目录访问权限限制本机连接，不能只依赖 secret 或 umask。

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

Agent 退出期间，到期调度会暂停；systemd 自动恢复和启动校正用于缩短并处理该窗口。不能宣称 Controller 离线容错等同于 Agent 永久故障时仍保证准时撤销，此边界需纳入故障测试。

### 6. 数据与资源控制

- Controller 使用本机 SQLite，启用 WAL，采用短事务并控制写入并发；不通过网络文件系统共享数据库。备份使用一致性备份方式，并包含恢复所需密钥，不能只复制运行中的主数据库文件。
- 数据库保存业务模型和修订关系，Mihomo YAML 是生成物，不作为唯一业务数据来源。Listener 引用变更及端口释放需幂等处理，收到节点确认后才能回收资源。
- Agent 本地只保存必要状态、配置版本和轻量计数检查点；启动、到期与重载操作应共用一致的状态更新机制。
- 资源、网络、RTT 和用量按需查询，不持续扫描全网或同步资源指标；只保留必要心跳、到期执行、证书维护和日志清理。
- 探测与发布限制并发并设置超时；页面关闭后停止本次页面触发的刷新任务。缓存过期本身不触发全网自动探测。
- 日志沿用产品默认值：warning/error，单文件 5 MiB，最多 4 份（含当前），总量不超过 20 MiB，最长 7 天；systemd 日志需另设相应限额。
- 内存、空闲 CPU、磁盘增长、连接规模和发布期间峰值在原型完成后实测，再形成部署规格。

### 7. 首轮开发顺序

1. 建立 Go Controller/Agent 与 Vue 前端工程，完成静态资源嵌入、SQLite 迁移和英文 i18n 基础。
2. 完成节点入网、mTLS WebSocket、断线重连及任务幂等机制。
3. 打通独立 Mihomo 安装、配置校验、API 重载、运行确认和失败回滚。
4. 验证四种协议的 Listener 行为、配置重载影响、身份撤销、到期与进程恢复，再确定首个交付内核版本。
5. 实现线路、订阅、引用回收、规则与 DNS 生成，然后接入按需推荐和出口检查。

原型优先证明单节点发布与撤销闭环，再扩展多跳。此处仅记录计划，不表示对应功能已完成。

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
- **Mihomo 隔离：**项目测试内核固定 v1.19.33，安装到 `/usr/local/libexec/meshstrata/mihomo-v1.19.33`，通过 `meshstrata-mihomo` 调用。保留宿主机已有 `/usr/local/bin/mihomo`、运行进程和监听，不安装项目常驻服务。
- **前端验证：**临时工程使用 Vue 3、TypeScript、Vite、Naive UI 和 Vue I18n，类型检查及生产构建通过。
- **后端验证：**sqlc 生成 SQLite 访问代码；使用 `modernc.org/sqlite` 1.62.0，在 `CGO_ENABLED=0` 下构建通过，数据库插入/查询和 Go 嵌入前端的 HTTP 响应检查通过；SQLite CLI 完整性检查通过。
- **内核验证：**使用独立工作目录和完整配置执行 Mihomo `-t` 校验通过，未启动新的监听。此检查不覆盖四协议转发、配置热重载或生产部署验收。
- **软件源处理：**原系统 HTTP 软件源更新无响应，本次临时指定 Ubuntu 官方 HTTPS 源安装 SQLite，未修改原软件源文件。
- **文档：**新增[开发环境说明](Meshstrata_开发环境.md)，记录精确版本、安装路径、校验摘要、前端依赖版本及验证边界。
- **下一步：**按已确认顺序建立最小 Controller、Agent 和前端工程，在工程依赖与锁文件中固定版本。
