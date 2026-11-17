# 公共契约（v1，I00复核，2026-11-12）

本目录是 HTTP/控制协议的代码旁契约。正式安全模式已注册认证、节点管理、入网控制、产物传输及任务管理，`x-implemented: false` 表示预留。历史阶段行为见文末对应阶段说明。新增认证及业务行为必须经过对应阶段，不能以空数组/空任务的 200 代替未实现。健康检查沿用 README。

## 版本、ID、列表、错误与任务

- `internal/domain` 是 Controller/Agent 公共 Go 模型。ID 为 `<kind>_<128-bit random hex>`，不可按名称、地理位置或语言重建。已有 `instance_id` 保持原格式与原值，不迁移为业务 ID。前端 demo ID 不上传 live。
- 正修订为 1..2^53−1 的 JSON 整数。写操作必须带 `expected_revision`、1..128 字符 `idempotency_key`；创建的前置修订为 0。更新使用短事务 CAS，冲突 409 `REVISION_CONFLICT`，带 `current_revision`，不覆盖用户草稿。B00 `Store.RenameNode` 实现 CAS 示例；它未对 HTTP 开放。B02 必须在接入该方法前补齐 HTTP 写幂等账本。
- 列表 `limit` 默认 50，范围 1..200；`cursor` 最长 512 字符，不透明、无敏感信息、按稳定 ID 续页。返回 `{items: [], next_cursor: null}`；不保证跨并发编辑的快照分页，不能用列表页长度推断全局总量。sqlc 的 nil 列表由 API mapper 转成 `[]`。
- 错误外层 `{error:{code,fields?,current_revision?}}`；字段位置是 JSON pointer（如 `/links/0/listener_id`），参数只允许非敏感白名单。`INVALID_ARGUMENT` 400；`UNAUTHENTICATED` 401；`FORBIDDEN` 403；`NOT_FOUND` 404；修订/幂等/引用/端口冲突 409；`TASK_EXPIRED` 409；确需注册占位时 `NOT_IMPLEMENTED` 501。日志不得打印整个请求/错误参数。
- 异步命令返回 202 `{task_id}`，实际结果从管理员任务读接口获取。`Task.state` 不替代逐节点 Applied、Verification、Rollback、Cleanup。取消仅限尚未应用的阶段；应用后的恢复是独立 rollback 任务。部分节点离线不能推导整批成功。HTTP 层鉴权、限流、CSRF、任务调度、完整业务写幂等属于 B01/B02/B04，B00 的账本仅持久任务意图及结果。

## 四种信任域

| 平面 | 传输与身份 | 所有者 / 禁止混用 |
| --- | --- | --- |
| 管理网页/API | 8443 HTTPS，B01 服务端 opaque 会话，Secure/HttpOnly/SameSite cookie；写操作 Origin + CSRF | 会话不能下载为代理凭据；初始化需可信本地 bootstrap，绝不可成为公网抢占入口 |
| 订阅下载 | 普通 HTTPS，独立 download-only 能力 token；hash验证，B01密文/引用支持再次复制 | B08；标准 Copy URL 使用 `/subscriptions/v1/config/{token}`，无需请求头。可选 `/subscriptions/v1/config` Authorization bearer 入口语义一致。每次验证到期/撤销，重置不换仍有效的入口身份；专用能力链接的处理见下文 |
| 首次入网 | 独立 9443 服务端 TLS，先验证预置信任 CA/指纹及域名/IP，再在 JSON body 发送一次性短期 token + CSR | B02；OpenAPI 用 `x-authentication` 描述 body token，不虚构 OpenAPI 的 body apiKey。私钥留节点；一次性消耗与签证关联须原子、支持安全重试 |
| 后续控制/产物 | 9443 mTLS，证书绑定 node_id 且节点未移除；WebSocket 子协议 `meshstrata.control.v1` | B02；不能接受管理员/订阅/入网 token 代替节点证书，不能只比较消息 node_id |

证书用途分开：内部管理 CA、业务 TLS、REALITY、frp 隧道、DNS API、IPinfo、订阅代理身份。B01 提供加密存储，`credentials` 仅留 key_id/ciphertext 与用途；B00 不提供加密实现或存入假密文。普通 DTO 只有引用，不得透传 sqlc credential 模型。节点本地 Mihomo secret 永不上传 Controller；浏览器本地配对 secret 仅内存，不放 URL、localStorage、草稿、日志。公网证书首接 Cloudflare DNS-01 并保留导入；位置首接 IPinfo并保留手工覆盖，B13 实现。

## 控制消息和账本边界

`internal/control`、`control.schema.json` 定义 version=1 的 command/result/receipt/hello。Go `Decode` 限 1MiB、拒绝未知字段/多余 JSON/未知版本/多 payload，校验 mTLS 节点与内外层身份一致。WebSocket ping/pong 仅保活；无 CPU/用量/日志的持续上报。传输实现必须先限制帧大小再解码；不支持的版本关闭并报告错误，不能静默降级。

- Command 含任务 ID、节点 ID、动作、目标修订/内容 SHA-256、截止时间、幂等 key、依赖节点及撤销 epoch。`depends_on` 是本任务内先完成运行确认的节点集合，Controller 校验无环并持久化。普通多跳先末端/后续节点再入口；反向线路按远端 Listener→Relay 授权/映射→映射认证转发→入口→完整线路确认。B00 账本验证 DAG，不执行调度。
- `ArtifactKey` 以 task/node/目标修订/摘要定位不可变私有配置包；B04 实现 `ArtifactSource`，B02 通过 mTLS 只向账本指定节点传输，B05 在落候选前校验大小与 SHA-256。`GET /control/v1/tasks/{task_id}/bundle` 在安全模式启用，无 secret 查询参数。包固定 Listener、规则、Target DNS、出口及授权修订向量。
- Controller SQLite `tasks/task_nodes/task_dependencies` 是任务意图与逐节点证据账本。`EnqueueTask` 在一个事务写入完整目标/DAG，同 scope+key+规范化内容回原任务，不同内容报 `IDEMPOTENCY_CONFLICT`。规范化排序不改调用方切片；重放已超时同内容任务可读取旧 ID，新任务已过期拒绝。scope 必须由服务器认证上下文生成，不能信任浏览器提交。B04 负责同节点操作串行、阶段/截止时间、依赖确认和最终发布决定。
- Result 含独立 Applied/Verification/Rollback/Cleanup、目标修订/摘要、确认时间、单调 sequence 和稳定错误码。`RecordResult` 在事务内拒绝错目标、同序号不同内容，忽略较旧序号；只写证据，不推进发布状态。迟到结果可留作恢复证据，B04 不得因此推进已失败/取消任务。receipt 只在数据库提交后发出；重连后对相同结果重复发 receipt，不能重复减少引用。
- Agent 的 `LocalState` 接口由 B05 实现：候选、已应用、已验证、启动、上一可用摘要独立保存；检查点、授权墓碑与待确认结果 outbox 原子持久。授权墓碑以不可变identity_id为键；稳定entry_id可在合法续期时关联新身份代次，但旧身份永不重新授权。先持久再确认执行，先处理到期再启动/重载。接 receipt 才移除相应 outbox；hello 携最近应用摘要/撤销 epoch 和最多 100 个未确认结果，分批恢复，不能上传全量指标。证书续期不重置节点检查点。
- 发布/回滚/重连/旧启动配置均合并本机单调撤销集合与有效期，不能因旧快照授权已撤销/过期条目。下载 token 的撤销与入口撤销分别确认。B00 `Authorization.Active` 提供到期边界，真实本地到期执行留 B05；不能据此声称离线到期已实现。

## 关系模型和不变量

`002_foundation.sql` 增量增加节点/协议/端口池/节点 DNS 修订、Listener/端口/引用、固定路径修订/逐跳、出口目标/策略修订/有序规则、DomainSet/有序规则、订阅/条目/组、私密凭据引用、任务/DAG/结果、按需测量、事件、设置。没有 Workspace JSON blob；规则原始文本是编辑来源，不取代结构规则；凭据密文是私密叶子值。

- 每 subscription×route 唯一entry，每个身份代次独占凭据；当前所有协议入口均隔离 Listener（唯一索引）。SS2022/Hy2不得放松；其他协议必须先通过固定版本独立撤销与存量会话隔离测试，再新增迁移放开。内部相同剩余路径可共享，下一跳 Listener/认证/策略/DNS修订均纳入摘要，不能只比较服务器 IP。
- Listener 引用用关系行派生计数，`ReleaseListenerReference` 只更新未释放行，重复影响零行。归零仍保留端口，只有 Listener 清理已确认、无有效引用且 reservation 为 pending_cleanup 才释放；触发器阻止绕过确认、重复激活已释放行。节点实际监听/会话消失的证据由 B05 校验，B04 负责把确认写入正确资源。引用新增、身份撤销及端口修改必须同一业务事务协调；不能把 SQLite 约束当成真实运行检查。
- 端口唯一索引按节点/精确地址/传输/端口限制。tcp_udp预留两行；B04必须另做 IPv4/IPv6 通配、双栈及系统已有监听冲突检查。固定冲突不能自动换端口。反向映射同样等 Relay监听/活动转发消失确认；B12补齐映射授权与清理执行，不能套用 Listener 的查询冒充隧道回收。
- 订阅entry的删除墓碑与entry_identities各身份代次的撤销墓碑不可清空。有效期内延期可更新仍有效且未撤销的身份；到期后续期须创建新identity_id/凭据代次并经发布确认，保留旧identity墓碑；已删除entry不可重新激活。B08/B05按此协调，不能把稳定entry_id当作可复活的凭据身份。跨订阅组成员/初始成员受同订阅外键限制；非空组、初始成员属于组及同策略修订由 B07/B08事务校验。
- Target DNS只在节点；Listener固定节点DNS修订，路径固定出口策略修订，规则固定目标修订和 DomainSet修订。业务生成/发布必须校验完整修订向量、唯一末尾MATCH、DNS启动无循环；数据库的单MATCH索引不能替代“存在且位于末尾”的校验。匹配出口失效即失败，不能静默DIRECT回退。
- `outer_transport` 与 `business_udp` 分离；Hy2使用UDP不等于业务UDP已开放。测量value可null，网络RTT/HTTP参考/混合/partial分开，保留方法、范围、时间、端点/配置修订、环境、样本及路径验证；96h成功、5min失败缓存只在下一次显式请求时使用/重测。

## 平台与后续所有权

`internal/platform.Manager` 接口无 systemctl硬依赖，systemd/OpenRC实现留B17。Ubuntu/systemd与Alpine/OpenRC都是正式部署平台，controller/node/both三角色与可选frps/frpc均须验收。Alpine运行预编译CGO=0自研程序与嵌入前端，无Node/Go依赖；外部Mihomo/frp需固定版本、musl/静态兼容性和摘要校验。独立程序/状态/配置/服务/socket；不覆盖已有实例，不按通用进程名杀进程，服务重启互不联动。服务日志及OpenRC监督器stdout/stderr也受限额，重复安装/升级失败恢复不能仅靠systemd机制。B00只有类型和要求，没有安装器、服务文件或Alpine运行实测。

迁移编号、查询、生成及公共文件冻结规则见[阶段任务书](../../documents/Meshstrata_剩余开发流程_v1.0.md)。

## I00订阅能力链接与再次复制

专用随机download-only token至少256位熵、URL-safe，只授权单订阅下载。它可位于HTTPS资源路径，OpenAPI使用普通path参数和`x-authentication`，不能虚构`in:path`的apiKey。管理员密码、会话cookie、入网token、节点私钥、DNS凭据、代理认证和本地Mihomo secret仍不得进入普通URL/前端路由/日志/草稿。订阅业务接口继续`x-implemented:false`并返回404，B08才注册真实下载。

刷新后显式Copy URL通过管理员会话、Origin和CSRF保护的`POST /api/v1/subscriptions/{subscription_id}/download-link`取回当前链接，响应仅`{download_url}`；普通列表/详情DTO不含token/URL。B08必须同时使用hash验证及B01 Cipher保存可恢复的密文/引用；不是刷新即重置。迁移010增加`subscription_download` purpose和`subscriptions.download_token_credential_ref`。I03-P 将引用目标冻结为专用 `subscription_download_credentials(id)`，保留旧 credentials/CA/FK；约束引用唯一、purpose 固定正确、订阅映射及 token_version 在短事务中校验。新token先加密，再原子更新hash/version/ref并撤销旧credential；删除同样撤销hash与引用；失败不留半次重置，不换仍有效的代理身份。B01基础允许purpose扩展，但010落地前不能持久该新增用途。

每次下载验证订阅expires_at/revoked_at及当前token hash/version，旧链接重置/删除及时失效；错误不得反射URL/token。链接获取和下载的所有响应（含错误）`Cache-Control: no-store`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff`；禁止重定向、第三方资源及分析。能力链接只可在显式复制/导入或下载操作中短时使用，不进入普通导出、访问日志（反向代理也须按路径模板脱敏）、错误、Referer、任务证据和分析，前端路由/草稿/持久存储同禁。B08/T03用脱敏证据测试标准URL导入、刷新再次复制、单订阅范围、到期/撤销/重置/删除和无泄漏。Header入口仅兼容选择，不是放行前提。

## I00阶段1冻结入口

- `internal/secretstore.Cipher/Binding/Sealed`：B01实现，B02注入消费；现有002 purpose、credential ID、key_id版本、密文边界及失败语义见代码。密钥只在Controller本地；密文只给仓储/加密备份，不能作为DTO/节点载荷。
- `internal/adminauth.Guard`：B01实现Read/Write；B02管理handler只通过此小接口组合，不导入B01具体包；9443 TLS入网/mTLS独立且不装cookie中间件。
- `controller/internal/storage.Database`：B01/B02 repository复用`*storage.Store`的Read/Write生成查询作用域。只向repository注入，不向HTTP层提供SQL；禁止另开库、访问私有db、嵌套事务或在回调内签证/加密/网络。
- 精确构造、独占路径、SQL/依赖提案、临时副本检查和I01接线见[阶段1交接](../../documents/backend-delivery/阶段1交接.md)。B03仅交付内核适配/证据边界，B05后续执行runtime。

## I01 已集成的安全入口

显式 `secure` 配置完整且依赖构造成功时，8443 HTTPS 注册 B01 auth 与 B02 nodes 管理路由；普通回环开发 HTTP 仍不注册这些接口。实现的方法、请求和响应以更新后的 OpenAPI 为准。auth initialize/login/password 不使用业务 Mutation：初始化依靠可信本地 proof + 持久 CAS 墓碑，登录发新 opaque 会话，改密码校验当前口令与服务端修订并原子撤销全部会话。所有其余业务写保持 expected_revision/idempotency_key。

初始化 proof 仅由本地 `prepare-local` 产生的受限文件提供；`initialize-admin` 从 stdin 读用户名/密码并正常验证 HTTPS CA/SAN。无 proof 的公网请求不能初始化。Cookie 固定 Secure/HttpOnly/SameSite=Strict；写操作须精确 Origin 和 X-CSRF-Token。认证错误还包括 RATE_LIMITED/429（Retry-After）与 INTERNAL_ERROR/500。普通 JSON 配置只含文件路径、版本和公开地址，不含明文密码/master key/token。

9443 为独立 TLS1.3 监听；只有精确首次 `/enroll/v1` 可以不带客户端证书。续期、控制与产物请求都需要有效、未移除节点的 mTLS 身份。CA PEM 必须通过可信渠道提供给 Agent，可附加指纹；指纹单独不能完成入网。产物 transport 已注册，但缺 B04 source/resolver，认证后返回501。Reverse 无可信 Relay 能力，创建返回501，即使 role=both 也不认定已经运行 Relay。

Agent `control.mode=control_only` 是阶段1有限运行边界：组件锁内持久 `stage1-control.json`，绑定 node_id、applied_revision=0、空摘要、初始 revocation_epoch=1 和零 pending；只允许新节点明确无业务状态。未知文件/其他节点/非初始状态均拒绝，不删除或归零。HelloSource 与 B05 Runtime 二选一；命令和 receipt 在该模式以 WebSocket policy violation 拒绝，不生成 result/outbox/Applied/Verified。Controller 只接受相同初始 Hello，未装业务结果消费者。B05 仍须实现先持久 checkpoint/tombstone/outbox 再返回结果、精确持久确认回执的完整 Runtime。

Controller/Agent 关闭分别取消 HTTP、续期与重连，并显式关闭 hijacked WebSocket；不操作 Mihomo/frp。移除只撤销管理身份并断控制，revoke task 保持 queued/cleanup pending。其目标摘要是管理清理意图，不是配置产物，后续 B04 不得据此投递或伪造清理成功。控制在线、Agent health 和业务 verification 始终分开。

## I02-P：阶段二私有配置包与离线证据

`internal/control/{bundle,state,message}.go`及[bundle.schema.json](bundle.schema.json)冻结可编译包格式1；[阶段2交接](../../documents/backend-delivery/阶段2交接.md)为B04/B05所有权/构造/接线的权威补充。此项是准备代码，没有注册发布API、安装006/007或启用业务runtime；现有control_only及缺产物认证后501保留。

- 仅`EncodeBundle`产生的紧凑UTF-8 JSON可传输，字段顺序为Go结构顺序，集合按ID升序，DNS优先次序保留；所有非optional字段必须出现（空列表为[]）。`DecodeBundle`先限1MiB、核验外部ArtifactKey SHA256，再严格解码/重编码逐字节比较；重复/未知/缺失字段、空列表null、尾随空白/内容、其他编码和版本均拒绝。最大128 Listener、512包内墓碑、256资源、DNS各8、协议列表各8；其余边界与跨字段规则由Go验证器约束。
- ArtifactKey在包外，整包没有自摘要字段；摘要覆盖实际传输字节，包含task/node/target revision/epoch及rules/DNS/egress修订向量。Pin还验证实际组件：规则默认动作JSON字符串、DNSPolicy结构、出口mode JSON字符串的SHA256。mTLS认证并授权节点，digest仅验证完整性，不授予读取权。B04对每次Open/Resolve复核活跃节点、真实task-node目标/权限；B05对下载字节DecodeBundle并BindCommand（含epoch）。
- 格式1仅支持单节点四协议独立身份Listener、节点级DNS（numeric IP bootstrap及numeric IP/HTTPS numeric-IP DoH）、显式DIRECT/REJECT唯一末尾MATCH和direct/reject出口。没有任意YAML、脚本、宿主路径、URL资源下载或多跳线路编译；B06–B08扩展须新格式及兼容策略。
- 包内PrivateCredential只承载Controller分配的入口password/UUID，绑定credential ref及唯一audience节点；普通API DTO/日志不得携带Bundle/Grant。TLS证书/私钥、REALITY私钥均为受限LocalMaterial ID+purpose，由Agent本地allowlist解析，节点管理私钥/本地Mihomo API secret没有可表达的purpose。B04负责私有产物加密落盘（B01 Cipher、独立绑定），B05负责本地包/渲染/状态的独立密钥与0700目录/0600文件及原子持久化；不得复用Controller DB/主密钥。
- 授权identity_id是不可变代次ID；generation为正整数，同identity的entry/listener/协议/凭据/到期不可改变，CheckGrant拒绝变形；续期创建新identity及更高generation。到期边界now>=expires_at立即禁止；revoked_at非null立即禁止。同identity墓碑永久合并，epoch只接受单调Controller值，本地到期不制造epoch。回滚/重启先从独立授权账本合并墓碑及最大时钟，再过滤所有启动候选；不因旧包或Controller离线绕过。FilterAuthorizations是纯辅助函数，不能代替持久状态机或真实撤销探测。
- Result新增可选`restrictions`（最多32）携带identity/listener/generation/reason/observed_at/epoch/effective_digest。它绑定**最初真实授权任务**的task/node/target，sequence仍按(task,node)持久递增；不能伪造任务、复用sequence更改结果或把到期表示为原授权重新Applied/Verified。B05永久保留Grant.Origin，在checkpoint+墓碑+证据outbox同事务落盘。B04持久存完整证据并验证原任务授权，再receipt；终态任务也可追加证据，不能覆盖原发布历史或新修订状态。现有stage1 Store对扩展结果明确NOT_IMPLEMENTED，避免丢弃证据后receipt。
- Hello仍最多100条PendingResults，且整个消息<=1MiB；B05按条数及实际编码字节分页，不截断/删除outbox；B02已有receipt耗尽后继续Hello机制。只收精确(task,node,sequence)receipt后清除对应项，重复/乱序receipt不删除其他项。本地限制失败同样持久证据，cleanup=pending且不宣称确认；失败关闭本平台相关入口并保持重试，不能继续过期授权。
- 兼容性：消息版本/WS subprotocol仍v1；新增字段可选，旧消息及control_only字节形状不变。旧严格v1对扩展结果会拒绝，因此I02启用业务runtime必须成对升级Controller/Agent，不能滚动混用旧Controller。当前未启用runtime，不向旧端主动发送扩展字段。LocalState/Checkpoint是无现存实现的内部准备接口，本轮将其细化为可持久化中间阶段的CAS DurableState，无外部ABI迁移。


## I02 正式阶段二接线

`GET /api/v1/tasks` 与 `GET /api/v1/tasks/{task_id}` 使用 Guard.Read。cancel/retry/rollback 写操作使用 Guard.Write，body 是原有 Mutation，`expected_revision` 指源任务账本修订。取消只限未 armed 且无清理意图的 queued publish/rollback。retry 和 rollback 创建新任务、新目标和五分钟截止时间；rollback 以成功历史配置为输入，经当前撤销/到期过滤。未确认的首次 Open 来源拒绝换 task 重试并返回 `GRANT_ORIGIN_UNCERTAIN`。受限的 format 1 计划通过独占状态锁的本地 CLI 提交，普通 HTTP 只接任务 DTO 和 Mutation。

Agent 的可选 Hello `runtime:true` 表示已经恢复的执行能力。旧 `control_only` 保留原 pristine Hello，可安全上线。控制上线不构成代理业务证据。Hello `applied_digest` 对应所报已验证修订的 Controller artifact；新可选 Result `effective_digest` 与 Restriction 的同名字段记录已核对的本机渲染字节。结果和离线清理证据持久化后才发 receipt。命令 cleanup 确认覆盖其全部待清理身份，精确的原授权 Restriction 可单独确认子集。格式 1 业务运行需要同代 Controller 和 Agent；旧 strict v1 Controller 不解析新增字段。细节见 [I02 集成报告](../../documents/backend-delivery/I02.md)。

## I03-P 阶段三离线边界

[bundle-v2.schema.json](bundle-v2.schema.json) 和 `control.EncodeBundleV2/DecodeBundleV2` 冻结新产物的私有强类型格式：逐 Listener 绑定确定下游或固定末端策略；入口有不可变授权，内部 link 有独立身份和永久 retirement；末端只表达有限的 CIDR/MATCH/目标/DNS。Go 验证为规范，包括固定字节 SHA256、数量和绑定；没有任意 YAML、脚本、宿主路径。schema 的 `x-runtime-supported` 为 false，`control.schema.json` 仍只声明运行格式 1，旧解码器拒绝格式 2。线协议、旧 Grant/恢复、API 注册与前端模式均未改变。I03 的格式协商、双格式账本、真实渲染及验证边界见 [阶段3交接](../../documents/backend-delivery/阶段3交接.md)。

B06–B08 使用同一 Controller 进程内契约。管理员读模型只看安全视图，下载才能读取本订阅的凭据；完整客户端配置与 proxy-provider 为不同输出。B08/010 使用 `subscription_download` 的独立密文 envelope、验证 hash 和当前版本引用，保留 002 既有 credentials/CA。标准 HTTPS 能力路径、受管理员 + Origin/CSRF 保护的专用 Copy URL POST、no-store/no-referrer 和无重定向约定不变。
