# Meshstrata 前端实施任务

**日期：2026-11-10｜所有实施会话：GPT-6.1 Sol / High**

交互依据为 [前端交互设计](Meshstrata_前端交互设计_v1.0.md)，能力边界依据为 [产品设计](Meshstrata_产品设计_v1.0.md)。本文件是可直接用于启动独立 Codex 会话的任务书，包含全部页面的归属、前置条件和验收目标。

F00 已以提交 `2476a77` 交付。用户唤醒设计会话后，已复核契约与构建，并补齐页面开发所需的共享字段。当前进入 P01–P10 页面实施轮：在固定接口和独占目录下并行编辑，统一使用 `controller/web/scripts/check-pages.sh` 的文件锁串行检查，Git 操作由设计/集成会话独占。具体会话及状态记录于 `documents/frontend-delivery/会话登记.md`；任务编号本身不代表已完成。

## 1. 共用执行要求

- 项目：`meshstrata`，主机 Aliyun-sz，目录 `/root/meshstrata`。使用该项目现有 checkout 与当前分支；用户未要求新 worktree，不创建彼此隔离而无法共享交付的目录。
- 新会话模型显式设置 `gpt-6.1-sol`，推理强度 `high`。如果宿主不接受，报告实际错误，不悄悄替换模型。
- 阅读本文件、交互设计的相关章节、产品设计相关章节，以及开发日志的工程/交付约定。保留用户和其他会话的变更。
- 范围为前端原型。默认离线 Demo，不实现 Controller/Agent/Mihomo/frp、业务 API、真实证书/探测/本地 Mihomo 配对。禁止把模拟验证写成真实成功。保留已有 Go 构建与嵌入目录。
- 默认界面为英文，i18n 覆盖表单、按钮、验证、错误、反馈与可访问性文案。沿用 Vue 3/TS/Naive UI/Vue I18n，尽量不增依赖。图标优先现有库能力或简洁 inline SVG，不依赖外部字体/CDN。
- 不只画静态卡片：按钮、取消、返回、过滤、表单、侧栏、草稿和模拟结果应有完整反馈；业务尚未实现的地方明确占位，不能有看似可用的死按钮。
- 各业务会话只编辑分配目录和自己新增的验收记录。跨目录需求写入交付记录；由当前设计/集成会话统一调整共享代码，不自行改他人模块或同一份全局语言文件。
- 默认不自行开启更多会话或子代理，不部署网站，不启动系统服务。需要预览时只用项目本地前端开发服务，按可用工具说明操作。
- 遵守开发日志中“检查后提交并推送当前分支”的项目约定。并行时期由集成会话串行处理 Git 提交/推送；业务会话不要同时操作 Git index。用户唤醒后启动任务时必须明确采用串行或并行的哪一种交付模式。

## 2. F00 前端基础设施

**建议会话标题：Meshstrata 前端基础设施与页面契约**

F00 已完成。目标是在后端未启动时运行前端，能通过真实 URL 访问所有计划页面，且后续业务模块拥有稳定的路由、组件、模型和演示数据契约。基础阶段不需要完成 P01–P10 的业务界面。

### 2.1 具体工作

1. 将现有 App.vue 中布局、导航、系统信息和占位内容拆开。按交互设计实现 Overview、Nodes、Routes、Client Config 二级导航、Activity，Settings 放侧栏底部。默认保留现有绿色浅色风格并改善键盘与窄屏布局。
2. 引入与现有 Vue/TS 兼容的 Vue Router，固定安装版本并更新锁文件；使用 hash history。实现交互设计的全部路由、动态对象参数、面包屑、活跃导航、404、页面标题、query 白名单和内部 returnTo 校验。保留前端输出到 controller/internal/webui/dist 的契约。
3. 创建第 2.2 节列出的所有页面文件及占位组件，入口可点通；占位明确显示 Pending implementation、页面目标和合理返回/关联导航。不要把这些当最终业务页。创建统一路由辅助函数，避免各页面自行拼接 URL。
4. 将全局 providers 放到应用层，包括 Naive UI message/dialog、主题、locale。建立基础排版/间距/颜色变量和公共组件。不要让 App.vue 继续承担业务实现。
5. 创建最小但可扩展的 domain 类型和可复用模拟 workspace：覆盖交互设计 4.2 的实体、稳定 ID、引用、时间、草稿/已发布修订、独立连接和验证状态。只实现供页面操作使用的读写命令基础，不在 F00 内完成所有业务算法。
6. 建立一组关联自洽的 seeds，含 Direct/Reverse/offline、路径/Listener/策略、规则/订阅/任务/用量。共用状态改变后所有订阅它的页面更新；演示数据重置有明确入口。提供失败/缺测/过期等可控场景，不能用随机成功率制造不可复现错误。
7. 非敏感 demo 数据与草稿可版本化持久化。secret、密码、代理认证、安装 token 仅内存；错误和下载不得泄露真实信息。演示域名与 URL 使用 example.invalid，模拟配置和结果标注用途。
8. 抽离通用 query 状态、草稿/dirty guard、日期数字格式化、可取消的一次性模拟任务能力。刷新保留路由和草稿；无周期请求；任务在卸载时清理计时器和订阅。
9. 公共 i18n 与各 feature/locales/en.ts 分开。建立清晰的命名空间装载方式，让每个业务会话只改本目录资源。默认英文，后续加语言不改配置数据。
10. 为 EgressPolicyDrawer 和 CheckEgressDrawer 建立可编译的占位出口与 props/emits 类型；route-editor、routes、subscriptions 后续依赖它们。具体类型在契约文档中列出，不把 UI stub 当业务校验器。
11. 原型默认不向任何 API 发请求。保留现有 getSystem 的真实只读能力，但只有明确的 Controller 信息查询入口才允许调用；Controller 不可用不阻断 Demo。不要把真实请求和模拟响应混在同一“已连接”状态。
12. 输出 `controller/web/FRONTEND_CONTRACT.md`：目录/入口、路由名称和 helper、组件 props/emits、domain 类型入口、workspace 读写用法、i18n 命名空间、草稿恢复、场景/重置、后续任务边界、运行与检查命令。这个契约是后续页面会话的前置资料。

### 2.2 预留入口文件

| 所属模块 | 必须创建的入口 |
| --- | --- |
| overview | `controller/web/src/features/overview/OverviewPage.vue` |
| nodes | `controller/web/src/features/nodes/NodesPage.vue`、`NodeEnrollmentPage.vue` |
| node-detail | `controller/web/src/features/node-detail/NodeDetailPage.vue` |
| routes | `controller/web/src/features/routes/RoutesPage.vue`、`RouteDetailPage.vue` |
| route-editor | `controller/web/src/features/route-editor/RouteEditorPage.vue` |
| egress | `controller/web/src/features/egress/EgressPolicyDrawer.vue`、`CheckEgressDrawer.vue` |
| subscriptions | `controller/web/src/features/subscriptions/SubscriptionsPage.vue`、`SubscriptionEditorPage.vue`、`SubscriptionDetailPage.vue` |
| rule-profiles | `controller/web/src/features/rule-profiles/RuleProfilesPage.vue`、`RuleProfileEditorPage.vue` |
| activity | `controller/web/src/features/activity/ActivityPage.vue`、`ReleaseDetailPage.vue` |
| settings/auth | `controller/web/src/features/settings/SettingsPage.vue`、`controller/web/src/features/auth/LoginPage.vue` |

模块内可以后续增加组件和 composable，以上入口路径保持稳定。全部页面 default export；路由从 URL 参数读取对象上下文。除 egress 外，各占位页面不应成为其他 feature 的业务依赖。

### 2.3 允许修改范围与验收

F00 独占 `controller/web/` 的基础改造、上述占位入口、`controller/web/FRONTEND_CONTRACT.md`，可以更新 README 的前端运行说明和追加开发日志。不得改产品设计或重写当前交互设计/任务书；发现冲突应记录在契约的“待决定事项”。不改 Go API、数据库、安装器。

至少通过 `npm --prefix web run typecheck`、`npm --prefix web run build`、`git diff --check`。运行可用的浏览器检查：一级/二级导航、刷新/前进后退、所有路由、无效 ID/404、草稿返回、Demo 重置、窄屏、无自动请求。对 query 白名单、持久化恢复或路由辅助等有实际价值的纯逻辑可以补少量测试；不增加排版快照测试。

F00 交付时明确区分已完成基础能力和仍是占位的业务页，列出文件、运行方式、检查结果、Git 提交/推送状态及后续风险。没有浏览器工具时如实说明未做交互实测。不要自行启动 P01–P10 或向原会话发消息；用户会唤醒原会话安排下一轮。

## 3. 业务页面会话任务

以下每项均建立独立 GPT-6.1 Sol / High 会话。创建时带上“共用执行要求”、本任务、契约绝对路径和选择的串行/并行 Git 规则。每项都覆盖关联的列表、编辑、详情状态，避免把紧密相关界面拆给不同会话导致交互不一致。

| ID / 建议会话标题 | 负责文件目录 | 设计依据 | 核心交付与验收 |
| --- | --- | --- | --- |
| P01 Meshstrata 总览与待办入口 | `features/overview/` | 3.1 | 三项可点击统计、待处理任务、最近操作、分阶段引导、手动快照刷新；空/失败/过期，精确跳转与查询范围。 |
| P02 Meshstrata 节点列表与接入向导 | `features/nodes/` | 3.2 | 列表搜索筛选/批量按需操作；Direct/Reverse 三步接入；固定 Relay 前提、不可执行安装示例、过期/重试、成功进入协议配置。 |
| P03 Meshstrata 节点详情与协议 DNS | `features/node-detail/` | 3.3 | 五个详情标签、独立连接状态、协议默认值与实际 Listener、端口冲突/依赖、统一 Target DNS、探测/隧道配置、节点移除影响。 |
| P04 Meshstrata 线路列表与详情 | `features/routes/` | 3.4 | 搜索筛选、路径拓扑/文字、版本与检测分开、资源引用、复制为草稿、进入编辑/出口检查/订阅、发布历史。 |
| P05 Meshstrata 四步线路创建与编辑 | `features/route-editor/` | 3.5 | 出口优先四步向导、三种测量模式、多候选命名、按需高级编辑、Reverse 固定限制、Review 和模拟发布任务跳转、保留已发布版本。 |
| P06 Meshstrata 出口策略与按需检查 | `features/egress/` | 3.6–3.7 | 线路侧栏，IP/CIDR 有序规则/唯一 MATCH、默认拒绝、目标类型/范围/引用/修订；四类检查结果、scope、时间、测试地址、Target-only 与局部能力说明。 |
| P07 Meshstrata 订阅与配置交付 | `features/subscriptions/` | 3.8 | 列表、四步编辑、详情四标签、规则选择往返、组与身份/端口预览、有效期/删除/token 操作、配置演示下载、同口径用量汇总。 |
| P08 Meshstrata 规则编辑与 DNS 映射 | `features/rule-profiles/` | 3.9 | 列表 CRUD、文本/规则解析、行级检查、MATCH 处理、目标插入、域名集合、DNS 推导/冲突、订阅上下文和返回草稿。 |
| P09 Meshstrata 活动与发布任务 | `features/activity/` | 3.10 | Events/Releases/按需日志、筛选、来源跳转；逐节点任务、Reverse 依赖、Applied/Verified 分开、失败/回滚/清理/离线待同步和重试。 |
| P10 Meshstrata 系统设置与演示登录 | `features/settings/`、`features/auth/` | 3.11 | 八组设置、局部保存/撤销、表单校验、Relay 条件、证书/日志/本地配对演示；英文单管理员登录、内部 returnTo、secret 仅内存。 |

所有文件目录均相对 `/root/meshstrata/controller/web/src/`。例如 P01 的完整路径为 `/root/meshstrata/controller/web/src/features/overview/`。每个任务将独立说明保存到 `documents/frontend-delivery/Pxx.md`，包括实现范围、实测结果、共享契约缺口、尚未接入的依赖；目录仅在产生交付时创建，不提前放空报告。

### 3.1 依赖与启动顺序

F00 完成后先检查其代码、契约和构建，修复阻断问题，再派发业务任务。模型与 reasoning 每次创建都显式指定。

1. 优先 P02/P03：保证节点列表和配置可以为线路流程提供真实的前端交互。
2. P06/P08/P09：实现出口侧栏、规则编辑、任务呈现，供后续流程调用。三者可在共享契约冻结且文件互不重叠时并行。
3. P04/P05：线路查看与创建编辑，接入 P06/P09；不把尚未完成的出口检查假装成功。
4. P07：贯通线路 → 订阅 → 规则往返和配置交付，接入 P08/P06。
5. P01/P10：可在上述期间独立实施，最后用共享实体汇总并检查导航。
6. 当前设计会话负责集成验收、共享修改、README/开发日志和 Git 串行交付；不另建一个没有用户授权的自动调度会话。

主机此前记录约 1.7GiB 内存，因此不要同时运行多个 npm 安装、Vite 服务或生产构建。页面编辑可以并行，但检查和 Git 操作需串行。若工具不能提供可靠的资源协调，则按上述依赖串行启动会话；不靠反复 sleep 轮询文件等待依赖。

## 4. 接口设计阶段的交接材料

原型完成后，从已实测页面行为整理接口需求，不在本轮提前伪造 API：列表查询/筛选、资源详情与依赖、草稿与发布修订、按需查询任务、批量部分失败、逐节点确认、撤销与清理、能力未知与观测时间。模型需要稳定 ID、错误 code/params、查询口径和任务状态，而不只是“保存成功”布尔值。

全链验收以交互设计第 5 节为准。最终至少演示：Direct 路径发布 → 订阅规则配置 → 模拟下载；Reverse 与出口策略；离线撤销/回滚失败；缺测与不同用量口径。UI 验收通过不代表后端协议、DNS 或隧道实现已验证。
