# Meshstrata 完整 v1 剩余开发流程

基线：2026-11-12（Asia/Shanghai）。产品依据为[产品设计](Meshstrata_产品设计_v1.0.md)、[交互设计](Meshstrata_前端交互设计_v1.0.md)、[开发日志](Meshstrata_开发日志.md)及[前端集成验收](frontend-delivery/集成验收.md)。公共协议见[代码旁契约](../controller/api/CONTRACT.md)、[OpenAPI](../controller/api/openapi.json)与[控制消息 Schema](../controller/api/control.schema.json)。本任务书覆盖完整 v1，不把离线原型或计划支持记为已交付。

## 执行与停止规则

1. 主协调会话只拆分、派发、审核。每批派发后停止，用户说“继续”再安排下一步；开发会话只完成自己的 Bxx，不自动启动后续阶段，不向其他会话发送消息。登记实际会话 ID，无法可靠获取写“待补”，不得推测。B00和本轮I00均为各批唯一写入会话，当前 checkout/分支，不建 worktree、不启其他会话或子代理。
2. 后续同目录最多三个开发会话并行。每项独占下表目录，公共文件只由当批独立集成会话修改。原工作区集成、sqlc生成、依赖/构建、Git index/提交/推送仅由集成串行执行；业务会话不执行Git写操作。阶段1业务会话仅能按[阶段1交接](backend-delivery/阶段1交接.md)在自己的.local/backend-check/Bxx临时副本应用本模块提案并检查，后端flock限制并发；前端沿用现有flock。不得复制其他正在编辑的模块、真实密钥/数据库或node_modules。
3. 每阶段开发交付后，新开独立集成会话审代码、串行接线、运行检查、提交推送；随后再新开对应 Txx 多机功能/故障测试会话。阶段1–6采用隔离测试部署脚本，7验正式安装器。B00和I00按各次授权独占Git并完成各自提交推送。
4. 测试服务器地址/凭据只能从忽略的 `.local/test-servers.md` 读取；不提交文件、不把凭据/命令中的secret复制到文档。用户已授权后续测试会话远程安装测试；B00不远程部署。现有确认环境为一台Ubuntu24.04 x86_64约4GiB/systemd、一台Alpine3.23 x86_64约435MiB/OpenRC。开发机约1.7GiB且有既有Mihomo，所有测试独立实例，不修改原服务、配置、端口或通用进程。
5. 小内存机串行安装/构建/协议测试；服务器只运行预编译产物，不安装Node/Go。客户端先以Linux固定版本Mihomo完成自动验收，再记录其他平台边界。B03锁定版本，不能把开发机已有Mihomo直接当作平台实例。

## 公共文件与数据库冻结

| 范围 | B00后负责人 | 修改方式 |
| --- | --- | --- |
| `internal/domain/*`、`internal/control/{message,artifact}.go`、`internal/platform/service.go`、`internal/{secretstore,adminauth}/*` | 每批独立集成会话 | 模块提出具体字段/接口变更及兼容性，集成审后统一落地；禁止并行改定义 |
| `controller/api/*`、`controller/web/FRONTEND_CONTRACT.md`、`src/data/{contracts,live,demo}.ts`、旧`src/api.ts` | 集成 | 模块可新增其私有API DTO/adapter，不能自行切换全局模式/删除demo |
| `controller/internal/controller/{app,http}.go`、`agent/internal/agent/app.go`、`internal/config/*`、各cmd入口 | 集成 | 各模块交付构造函数/Handler/接口及接线提案，由集成串行注册；先安装安全中间件再开放写路由 |
| `controller/internal/storage/{storage,database}.go`、`queries.sql`、`migrations/*`、`dbgen/*`、`sqlc.yaml` | 集成 | 已应用迁移不可改写，001/002冻结；sqlc输出只生成，不手改 |
| `Makefile`、`go.mod/go.sum`、前端package/lock、构建脚本、公共i18n/router/domain/components | 集成 | 依赖精确锁定；不削弱既有检查，新增门禁先实跑 |
| README、产品/交互/开发日志、本任务书、会话登记 | 集成 | 业务会话仅写各自`backend-delivery/Bxx.md`和精确依赖提案`Bxx.deps`；测试写`Txx.md`，集成汇总实际状态 |
| Git与发布产物 | 集成（本轮I00独占） | 保护用户修改，检查通过后提交并推送当前分支；失败保留本地提交并报告真实原因 |

后续迁移槽位固定为 **B01→003、B02→004、B03→005、B04→006、B05→007、B06→008、B07→009、B08→010、B09→011、B10→012、B11→013、B12→014、B13→015、B14→016、B15→017、B16→018、B17→019、B18→020**。每项只写 `controller/internal/storage/proposals/Bxx.sql` 提案（不被embed/sqlc扫描），集成按槽位顺序审阅安装为 `NNN_bxx.sql`，无变更写带原因的 `SELECT 1;`。保证迁移连续，不能提前提交带缺口的高编号；跨模块约束先联合审阅，不把整个Workspace塞成JSON。002交付前可修，交付后不可重写。额外迁移只能由集成更新登记并在020之后分配，禁止抢用其他槽位。

查询分文件：B02 `queries/nodes.sql`、`nodes.go`；B04 `queries/{tasks,references}.sql`、`ledger.go`；B01 `queries/settings.sql`（设置存储，不接管CA实现）；其余项新建唯一的`queries/bxx_*.sql`。任何人不得直接编辑dbgen；集成在一批SQL齐备、全部迁移槽位连续后执行`make generate`，再执行`make check-generated`。生成文件没有并行所有者。B05主要维护节点本地检查点，不能操作Controller数据库连接或重用Controller密钥文件。

## I00后的阶段1派发基线

[阶段1交接](backend-delivery/阶段1交接.md)是B01/B02/B03下一批的具体约定：精确独占路径、共同只读接口、构造/错误/事务边界、SQL/依赖提案和`scripts/backend_check.py`副本验证流程均已给出。001/002冻结，003/004/005归属不变。I01独占正式接线/迁移/生成/依赖锁定/全量验证。专用订阅下载能力token允许在HTTPS资源URL，不能再以“secret不进URL”泛化成强制header导入；B00历史报告保留原样，由I00纠正。

## 分阶段任务书

每个 Bxx 均须交付可编译实现、私有接口/接线说明、必要测试和实际证据，不接受纯建议。阶段1的可编译证据来自固定I00基线+本模块改动/SQL/精确依赖的临时副本；正式共享依赖、生成物、迁移与全量编译由I01在三个会话结束后串行完成，开发会话不得冒称已集成通过。以下路径均相对仓库根目录；没有列出的共享文件按上表处理。

| 阶段 / 项目 | 独占目录或文件 | 输入依赖与接口交付 | 开发验收及后续门禁 |
| --- | --- | --- | --- |
| 0 B00 公共基础 | `internal/domain`、`internal/control`契约、`internal/platform/service.go`、`controller/api`、迁移002、`src/data`、本任务书 | 现有健康/元数据与离线前端；交付稳定ID/修订、错误、列表、异步任务、控制消息、账本边界、关系模式与映射 | 新库/001升级、instance_id、关系/唯一约束、CAS、任务幂等/DAG、消息往返/拒绝、生成一致性；make check及前端含i18n；保留demo、无新自动请求 |
| 1 B01 管理登录/安全存储 | `controller/internal/auth/`、`controller/internal/secrets/`、`queries/settings.sql`、`queries/b01_*.sql` | I00冻结的secretstore.Cipher、adminauth.Guard、storage.Database及构造约定；向B02/B08/B13提供鉴权中间件、可信首次初始化、CSRF、服务端会话、加密凭据读写/轮换接口 | 单管理员密码哈希、暴力限制、退出失效、cookie/Origin/CSRF、初始化抢占防护；secret不在响应/日志/URL/草稿，加密密钥备份边界清晰 |
| 1 B02 入网/CA/mTLS | `controller/internal/enrollment/`、`controller/internal/control/`、`agent/internal/enrollment/`、`agent/internal/control/`、`internal/pki/`、`storage/nodes.go`、`storage/b02_*_test.go`、`queries/nodes.sql`、`queries/b02_*.sql` | B00消息与节点类型；I00 Cipher/Guard/Database（用fake并行编译，I01注入B01实现，9443不依赖cookie）；向B04/B05提供已认证节点通道和产物传输 | 发token前验证Controller、CSR私钥本地、一次性短期token并发消耗、证书续期/撤销、移除断旧会话、重连退避/身份绑定；不把控制在线标代理可用 |
| 1 B03 Mihomo/四协议 | `internal/mihomo/`、`agent/internal/mihomo/`、`scripts/compat/mihomo/`、`queries/b03_*.sql` | B00 Listener/能力/服务边界；给B05配置校验、重载、实际监听/认证/撤销探测接口；给B07/B08固定Linux客户端字段 | 锁定版本/摘要/架构；四协议真实最小转发、重载假成功、独立撤销与存量连接；SS/Hy2入口隔离，其他协议未经验证同样隔离；Unix父目录/缺配置/无通用入站、既有实例不变 |
| 2 B04 Controller发布/引用 | `controller/internal/releases/`、`controller/internal/ports/`、`storage/ledger.go`、`queries/{tasks,references}.sql`、`queries/b04_*.sql` | B01鉴权、B02通道、B03能力；实现任务编排/产物源及结果receipt；与B05冻结包格式/修订向量 | 同节点串行、先后续再入口、逐节点确认、Applied≠Verified、截止时间、DAG、CAS/幂等；引用减少幂等、零引用后确认清理才释放，通配/双栈冲突及固定端口失败 |
| 2 B05 Agent执行/检查点 | `agent/internal/runtime/`、`agent/internal/checkpoint/`、`agent/internal/authorization/`、`queries/b05_*.sql` | B00 LocalState、B02认证通道、B03内核；给B04运行证据和重放结果 | 候选/应用/验证/启动/上次可用分离，检查点/outbox原子；崩溃恢复、实际协议认证、回滚再次校验；本地到期/撤销墓碑优先，重启/回滚/重连不能复活；离线节点不伪确认 |
| 3 B06 固定路径/出口 | `controller/internal/routes/`、`controller/internal/egress/`、`queries/b06_*.sql` | B04/B05发布，B03能力；给B07/B08已发布固定修订与目标/策略引用 | 单节点/三节点、逐段Listener、内部共享匹配完整剩余路径；末端有序CIDR/唯一MATCH、默认REJECT、目标失效不回退、外部网卡/代理回环检查、DNS节点级 |
| 3 B07 规则/配置/DNS | `controller/internal/rules/`、`controller/internal/clientconfig/`、`queries/b07_*.sql` | B03锁定客户端、B06路径/出口、B08条目视图（接口并行）；输出完整配置和provider独立类型 | 保序解析/行列错、DomainSet纯域名/修订、唯一末尾MATCH、非空手动select组、同策略修订；DNS业务/节点域名/bootstrap分离，无循环/静默直连，默认TUN/fake-IP/IPv4/业务UDP拒绝 |
| 3 B08 订阅/身份/token | `controller/internal/subscriptions/`、`queries/b08_*.sql` | B01secretstore、B04引用/任务、B06路径、B07生成；向前端提供条目/同步与下载契约 | subscription×route独立凭据/入口，过期续期新身份代次保留旧墓碑；创建/续期/删除、下载token重置与入口凭据分离；过期/撤销链接即失效，节点离线Pending，独立撤销不伤其他条目；标准Copy URL导入及刷新后管理员+CSRF专用动作再次复制；010增subscription_download用途/密文引用，hash验证，Linux客户端实测 |
| 4 B09 登录/节点/设置前端 | `src/features/{auth,nodes,node-detail,settings}/`、`src/data/b09/` | B01/B02/B03/B13预留能力；只开放后端实际实现能力 | 真实会话、命令接入/到期重试、节点协议/DNS/位置/证书配置入口；未实现B13功能仍显式不可用，本地secret不上传；双语/草稿/窄屏 |
| 4 B10 线路/出口前端 | `src/features/{routes,route-editor,egress}/`、`src/data/b10/` | B04–B07接口与任务；与B09互不改公共字段 | 真实固定路径、审阅/差异/分节点确认/回滚、末端策略；未实现测量显示Not Checked；保留失败草稿及旧发布版本 |
| 4 B11 订阅/规则/活动/总览前端 | `src/features/{subscriptions,rule-profiles,activity,overview}/`、`src/data/b11/` | B04/B07/B08，后续B16按需观测接口 | 真实条目/下载/续期/撤销/清理、规则往返、活动来源/逐节点错误；未实现日志/用量不可伪成功，双语/窄屏；集成才切换全局live，demo独立保留 |
| 5 B12 frp反向出口 | `controller/internal/reverse/`、`agent/internal/reverse/`、`scripts/compat/frp/`、`queries/b12_*.sql` | B02管理身份、B04/B05编排、B06固定两节点；先验证锁定版本/授权机制再开发开放接入 | both固定Relay，远端只作出口，frps/frpc独立mTLS及逐节点名称/类型/端口授权；回环SS TCP映射、认证转发/业务UDP拒绝；移除关闭活动转发，重连不复活撤销，独立资源确认 |
| 5 B13 Cloudflare/IPinfo | `controller/internal/certificates/`、`controller/internal/location/`、`agent/internal/certificates/`、`queries/b13_*.sql` | B01加密凭据、B02 CSR/下发、B04发布；与B09设置页面经集成接线 | Cloudflare DNS-01首个自动证书服务商+导入，私钥本地/续证；IPinfo按需缓存且不盖手工位置；无域名/凭据真实记录阻塞，不拿mock当真实申请 |
| 6 B14 测量/HTTPS/推荐 | `controller/internal/measurements/`、`controller/internal/recommendations/`、`agent/internal/probe/`、`src/data/b14/`、`queries/b14_*.sql` | B02任务、B03能力、B06图、B13浏览器认可证书；B10提供接线提案 | 原始TCP/UDP与HTTP口径分开；短期限次探测授权/CORS/TAO/回显/无缓存/重定向拒绝，预热+串行3次；null不为0、混合/partial不混排，96h/5min缓存、取消/网络变更、无后台扫描 |
| 6 B15 出口检查/本地配对 | `controller/internal/checks/`、`agent/internal/checks/`、`tools/rtt/`、`controller/web/src/local-client/`、`src/data/b15/`、`queries/b15_*.sql` | B06策略、B07 DNS/客户端、B14测量口径；本地Mihomo仅浏览器按需使用 | Test Destination/回显/DNS/覆盖独立范围；Target-only显式、不可观测为null/Not Checked；本地secret仅内存，来源限制，Apply New/Reconnect独立；原始RTT工具可选且方法可追溯 |
| 6 B16 用量/资源/日志 | `controller/internal/observations/`、`agent/internal/observations/`、`internal/logrotate/`、`src/data/b16/`、`queries/b16_*.sql` | B03能力、B05检查点、B08身份；B11前端接线提案 | 入口只计一次，历史累计/连接次数/活跃快照/Unavailable分开、覆盖期/重置；资源按需短采样，日志尾部/字节上限，5MiB×4/20MiB/7天，限时debug恢复，无逐请求浏览历史 |
| 7 B17 安装器/服务 | `scripts/install/`、`packaging/systemd/`、`packaging/openrc/`、`internal/platform/systemd.go`、`internal/platform/openrc.go`、`queries/b17_*.sql` | B01初始化、B02接入、B03/Mihomo、B12/frp、B16日志；先与B18冻结产物manifest/目录/升级锁 | Ubuntu/systemd与Alpine/OpenRC并列正式支持；两平台各验controller/node/both及可选frp，安装/启动/独立生命周期/日志/重复安装/失败可重试，预编译无Node/Go依赖，不覆盖既有实例 |
| 7 B18 产物/升级/备份 | `internal/artifacts/`、`agent/internal/upgrade/`、`controller/internal/backup/`、`scripts/release/`、`documents/deployment/`、`queries/b18_*.sql` | 所有业务稳定状态、B17安装接口；交付签名/摘要manifest、兼容矩阵、恢复说明 | CGO=0/musl/静态产物和外部二进制兼容；升级校验/原子切换/回滚、数据库一致备份+密钥恢复、旧库升级失败保留状态；Ubuntu和Alpine均真机恢复，不把计划写已实现 |
| 8 T08 完整v1验收 | `documents/backend-delivery/T08.md`、`scripts/acceptance/` | T01–T07已通过的提交与真实阻塞记录 | 新开专门测试会话，逐产品第4节及补充平台要求核对；返修后测新提交，全部必需项通过方可完整v1交付 |

`src/`在前端行中均指`controller/web/src/`；SQL queries行指`controller/internal/storage/queries/`。阶段5/6后端新增功能由当批集成按前端既有入口接线，不能让B09–B11长期保留假live成功或让后端接口无人可操作。

## 独立测试任务书

| 关口 | 必测真实场景 | 放行条件 |
| --- | --- | --- |
| T01（阶段1后） | 假CA/错误域名首次信任拒绝、token过期/重放/并发/断线重试、真实入网/续期/移除断旧通道、网络中断重连；Linux固定Mihomo四协议认证/真实转发/重载、独立撤销及不影响其他条目 | 固定客户端/内核版本与摘要有证据；区分控制健康与代理成功；B01/B02/B03接线后独立测，不把单测或mock当入网成功 |
| T02（阶段2后） | API假成功但Listener失败、真实端口冲突/通配/双栈、节点崩溃/控制重启/重放、离线节点、跨节点顺序、运行验证失败/回滚失败、到期时Controller离线、Agent停机再启动与旧快照回滚 | 所有逐节点确认/待同步/回滚状态真实；端口仅确认清理后释放，重试引用不重复减，到期/撤销不复活 |
| T03（阶段3后） | 单节点和三节点固定转发；两订阅同线路独立撤销/到期/续期/token；内部共享回收；末端HTTP/SOCKS/网卡、DNS目标解析/客户端分流、目标失效无默认回退、UDP拒绝与Hy2外层UDP分开 | 三节点资源不足时在隔离主机追加第三实例并证明独立，不能以两节点替代；少量真实业务/回显、客户端完整YAML+provider、普通Copy URL导入/刷新再次复制/旧链接失效；header仅可选 |
| T04（阶段4后） | 浏览器真实登录、命令接入、节点设置→线路→出口→发布→订阅/规则→客户端→活动；离线/失败/过期、刷新与返回、双语、草稿、390px窄屏、网络请求审查 | 真实页面不混demo成功；无secret草稿/前端路由，专用能力链接按契约处理，权限由服务端；保留中英文与全部入口 |
| T05（阶段5后） | frp mTLS与逐节点映射授权/越权、回环暴露、经映射认证与转发、UDP拒绝、断线/Controller或Agent重启/移除后重连撤销、两端引用清理；Cloudflare真实签发续期/导入及IPinfo/手工覆盖 | frp版本和授权机制先证实；缺域名/凭据明确Blocked并列剩余步骤，不伪造证书成功；纯controller不能承接Relay |
| T06（阶段6后） | 测量方法/环境/修订/缓存/失败TTL/取消/网络变更、HTTP时序受限与null、无后台扫描；出口不同目标/target-only/DNS/IPv6/UDP覆盖；本地配对秘密、入口一次计量/覆盖期/重置、日志总量/时间/debug自动恢复 | 不混RTT/HTTP/partial，无大流量压测；关闭页面停止查询；不可用指标明确，测量不触发自动改线 |
| T07（阶段7后） | Ubuntu/systemd与Alpine/OpenRC各controller/node/both；各角色重复安装、启动、独立停止/重启、日志限额、升级失败/回滚/备份恢复；适用两端各可选frps/frpc生命周期、musl/外部二进制、已有实例共存 | 不能仅验Alpine node或仅做容器/构建。小内存机串行、用预编译包、无Node/Go运行依赖；不以systemctl作为唯一运行实现 |
| T08 | 从干净产物/恢复库分别按产品清单逐条复核，含平台、四协议、反向、DNS、撤销、UI、日志与资源边界 | 每条记录实现提交、环境、命令/结果与证据路径；未执行/模拟/无法观测不可勾选真实成功 |

## 前端操作—服务端任务—持久模型—所有者—测试映射

| 操作/入口 | 计划API或控制任务（业务尚未实现者不注册） | 持久模型/确认点 | 后端 / 前端 | 测试 |
| --- | --- | --- | --- | --- |
| 登录/首次初始化/退出/改密码 | `/api/v1/auth/{initialize,login,session,logout}`；密码接口B01补齐 | 管理员/会话/加密凭据，B01迁移003 | B01 / B09 | T01/T04 |
| 生成接入命令/等待加入/移除 | `/api/v1/nodes/{id}/enrollment`、`/enroll/v1`、`/control/v1`、remove任务 | nodes，token/管理证书由B02迁移004；节点身份与CA独立 | B02 / B09 | T01/T04 |
| 节点基本资料/协议/端口/Target DNS | nodes GET/PATCH；`test_protocol`、DNS变更publish | nodes/protocol_defaults/port_pools/node_dns_revisions/listeners/ports/task_nodes | B02/B03/B04 / B09 | T01/T02/T04 |
| 节点资源/位置更新 | `query_status`；位置按需请求 | nodes手工覆盖、measurements或观测快照，IPinfo密文引用 | B13/B16 / B09 | T05/T06 |
| 新建/编辑线路与批量候选 | routes草稿/修订API由B06定义，`publish`；各线路独立任务 | routes/route_revisions/route_hops/listener_references/task DAG | B06/B04/B05 / B10 | T02/T03/T04 |
| 出口侧栏/共享策略/检查 | egress目标/策略API；`check_egress` | egress_targets/revisions/policies/rules、固定策略修订；不存线路DNS | B06/B15 / B10 | T03/T06 |
| 创建订阅/条目/分组/配置预览 | subscriptions/entries/config-preview API；publish | subscriptions/entries/entry_identities/groups/members/credentials，单entry独立身份代次 | B08/B07/B04 / B11 | T03/T04 |
| 续期/删除/重置下载token | expiry/revoke/reset-token API，revoke/cleanup任务 | entry墓碑/到期、token hash/version、引用与逐节点确认 | B08/B05/B04 / B11 | T02/T03/T04 |
| Copy/Download/Provider | `/subscriptions/v1/config/{token}` HTTPS能力链接直接导入；可选header入口；管理员+CSRF的POST download-link动作支持刷新后再次复制 | hash验证+B01密文可恢复引用（B08/010）；普通DTO无token/URL，no-store/no-referrer/无重定向，生成物只含本订阅身份/公网入口 | B08/B07 / B11 | T03/T04 |
| 规则文本/DomainSet/往返订阅 | rule-profiles/domain-sets API与生成校验 | rule_profiles/revisions/rule_lines/domain_sets/revisions/members/引用 | B07 / B11 | T03/T04 |
| Activity任务/取消/重试/回滚/清理 | `/api/v1/tasks`及详情/actions；control result/receipt | tasks/task_nodes/dependencies/events；Applied/Verified/rollback/cleanup分离 | B04/B05 / B11 | T02/T04 |
| Reverse节点/线路 | reverse API；远端Listener、Relay授权/映射、入口发布顺序 | reverse_tunnels/tunnel_references/ports、两端独立确认 | B12 / B09/B10 | T05 |
| 证书与位置设置 | certificates/import/renew；location/settings | settings/credentials、节点本地私钥、证书记录由B13迁移015 | B13/B01 / B09 | T05 |
| Measure/Remeasure/Cancel/Network Changed | `measure`、recommendation API，短期HTTPS探测授权 | measurements缓存scope/method/environment/revision/TTL | B14 / B10 | T06 |
| Check Destination/IP/DNS/Coverage | `check_egress`，本地配对后的请求 | 检查结果按地址/范围/策略修订，B15扩展，value可null | B15 / B10/B11 | T06 |
| Local Client Pair/Apply New/Reconnect | 浏览器本机Mihomo，显式操作；不经过Controller上传secret | 仅非敏感配对偏好，secret内存；可选原始RTT工具 | B15 / B09 | T06 |
| Query Usage/日志尾部/资源 | `query_usage`/`read_logs`/`query_status` | Agent轻量计数检查点、覆盖期/重置、受限日志；B16扩展 | B16 / B09/B11 | T06 |
| 安装/升级/备份恢复 | CLI与独立service Manager，非任意HTTP shell执行 | 状态目录/manifest/备份密钥、安装/升级恢复点 | B17/B18 | T07/T08 |

这些API名称中只有OpenAPI标为implemented者当前可用；表内owner后续须在接线前补齐其请求/响应schema与权限，不能只实现前端调用。前端camelCase↔wire snake_case由`src/data`投影，`reverse_tunnel`→演示兼容`reverse`；task/result的逐节点字段不压成单布尔online。详情未提供的字段不能用demo补齐成真实数据。

## 统一验收、证据与返修

开发交付报告至少含实际改动入口、接口输入输出、迁移/查询提案、真实检查命令/退出结果、未完成范围/外部依赖、接线步骤和风险。集成保留原报告，不把某开发会话未执行的检查补写成该会话成功。每批独立集成必须在原工作区执行`make check`、完整`check-pages.sh`（含i18n）、`git diff --check`；业务会话只在许可的独立副本做针对性检查并注明位置；新SQL必须生成一致性，新行为补必要的故障/状态测试。构建与单测不代表多机或浏览器真实成功。

测试报告按条目记录提交号、两端实际版本/摘要、环境/角色/init、真实步骤、结果/证据、不支持/阻塞/未执行。禁止上传秘密日志、完整配置私钥、入网/订阅token；本地证据保留忽略路径，提交脱敏复现过程。测试前确认独立程序目录/状态目录/服务/端口，无权借现有实例省略安装验收。

失败返回原所有者定点修复，不让测试会话临时越界修改兄弟模块/共享定义。修复后独立集成检查并提交推送，再由对应T会话针对**新提交**重跑失败项和相关回归；不得用旧提交成功覆盖新失败。涉及公共契约/SQL的返修须更新schema/映射/生成并复核后续依赖。阻塞域名/凭据/机器条件时准确列出阻塞范围，继续可独立完成的验证；必需项未过不放行完整v1。

当前交付状态只以[会话登记](backend-delivery/会话登记.md)和各B/T实际报告为准。I00修正B00后的阶段1接口及验证流程已冻结（见[阶段1交接](backend-delivery/阶段1交接.md)与[I00报告](backend-delivery/I00.md)），B01/B02/B03可按相同I00最终提交并行；必须等用户在主协调会话说“继续”才派发；本会话到此停止。

## I01阶段1集成状态

B01/B02/B03已合入正式003–005/依赖/sqlc，显式HTTPS认证、CA、入网、续期与控制连接已接普通二进制。Agent只提供持久初始状态的control_only运行，拒绝业务命令，不等价B05。具体检查与限制见[I01](backend-delivery/I01.md)，后续独立验收按[T01计划](backend-delivery/T01计划.md)。T01只做阶段1真实控制和内核协议及Ubuntu/Alpine基础运行；完整发布到期/回滚T02、Reverse T05、三角色systemd/OpenRC安装及frp T07。本批停止，不自动开T01/阶段2。
