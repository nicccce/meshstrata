# T02 失败归因与阶段三依赖

针对固定产品 `e31787e9d75da298d73c6950852b497ea279d3f5`，范围和阳性记录见 [T02](T02.md)。证据目录为本机忽略路径 `.local/t02-1117a`。以下“待证”表示本轮没法确定成因；所有失败及未运行项保留原状态。

## expiry

**P0 定点验证：授权到期边界，结果未确认。** `topology-alpine3.json`、`timeline-alpine3.jsonl` 与 `alpinea3-expiry-private-error.log` 指向 Alpine Controller / Ubuntu Agent 的短期 Trojan 发布。任务先成功，Alpine 侧 helper 打开两条存量连接，Controller 停止后观察。源码的前一断言已走过：旧连接为 `closed`，另一身份的同一连接仍回显；失败的是随后的新连接字符串必须为 `closed`。异常没有打印实际值，本地证据没有 helper 最后一条 JSON。端口是否关、PID 是否保持、旧授权记账/端口 release、回滚和复用都没走到本轮断言。

“到期后旧凭据不能再转发”是功能要求，`fresh_old == closed` 只是一个可能的拒绝形态。helper 的 `observe` 也可在 SOCKS 建连被拒时返回 `socks_failed`，其余选项含 timeout 或真实回显；单凭不等于 `closed`，既不能证明旧凭据仍可用，也不能把所有非回显一概当作明确拒绝。它只在存量连接首次关闭时做一次新尝试，未把业务拒绝、探测故障和到期收敛期分开。此前该拓扑八项协议均成功，未见前案污染；此案提前退出后在下个用例留下了发布状态。

最小后续：先修本地观察器，让失败也落盘，分别确认拒绝、保留身份持续回显、入口/受管 PID 和 pending/confirmed/released 的次序。若发现越过到期边界的成功业务回显、影响其他身份或提前释放端口，先修 B05 本地转换或 B04 确认接线。Ubuntu Controller / Alpine Agent 的同类阳性及双平台冷启动过滤不能消除这条不确定性。

## wildcard

**P1 默认部署兼容性；产品验证路径可疑，机制待证。** `topology-{ubuntu,alpine,alpine2}.json` 的三轮初始发布在下游已 Applied 后失败（`verification=failed, rollback=failed, RELEASE_FAILED`），另一目标未 Applied；最早两轮覆盖两个 Controller 角色。先前协调记录把这些 `0.0.0.0` 尝试与 `owned socket inspection` 联系起来；本地拓扑、时间线和异常尾只留终态，无对应的原始 inspection 子原因、PID/FD inode 表或 `ipv4_wildcard_reproduction` 事件。静态核查：B03 [Inspect](../../agent/internal/mihomo/evidence.go) 将四张 proc 表解析为地址字符串，监听匹配逐字比较；B05 [exactSockets](../../agent/internal/runtime/apply.go) 再作精确匹配。若内核把预期 IPv4 通配实际放到双栈 socket，`0.0.0.0` 与 `::` 比较会失败；同一错误还可来自 API 归属校验或监听确实缺失。`rollback=failed` 在初次无已确认基线的发布也可能是没有可确认的旧渲染，不能据此证明曾可用的配置丢失。

实际入站 IPv4 在两平台交叉分配后有明确 Verified/DAG 成功记录，是当前可用的开发配置，未证明默认 wildcard、多网卡/地址变化、IPv6 或双栈部署。最小后续为本地地址族及归属测试：按真实 socket 家族和 v6only 语义核对期望覆盖，维持 PID/inode 检查和 foreign socket 排除；不要把 IPv6 通配一律当作 IPv4 通配。只有证实运行时监听形态才改匹配规则；第二处精确比较也需一致。

## foreign_and_reject

**P1 正常功能会遇到真实端口冲突；两拓扑分别失败在不同阶段。**

- **Ubuntu Controller / Alpine 下游。** `ubuntu-foreign_and_reject-private-error.log` 对应 `topology-ubuntu2.json` 中成功 TTL、管理回滚和新身份端口复用之后。helper 持有回环 TCP，计划又为精确地址端口发布 Listener；Controller 接纳，任务终态 failed，下游 Applied、verification failed，**rollback failed**。拒绝新配置是预期；有原可用配置时确认恢复是合理的功能要求。日志缺 Agent apply/restore 的细分错误和旧业务回显，无法判定是真的未恢复、恢复了但验证失败，还是 fixture 干扰。B05 [rollbackFailure](../../agent/internal/runtime/execute.go) 按实际 `restore` 成败上报；报告为 failed 本身不代表回退实现错误，也不证明普通服务已恢复。断言在下游 DTO 停住；该用例后续的上游未 fetch、foreign 进程/监听保持、retry 的 `GRANT_ORIGIN_UNCERTAIN` 都未验证。后来错 key 的保护实验使用同一 Agent，故障前检查点/渲染的状态不是独立干净基线。
- **Alpine Controller / Ubuntu 下游。** `alpinea3-foreign_and_reject-private-error.log` 和相邻 `controller-submit-release-1794884272011956240.log` 在 10:57:52（北京时间）对应 CLI exit 1、`DEPENDENCY_IN_USE`，时间线无该次成功提交。前面的 expiry 已提交短授权，失败前未核对清理确认；驱动只有完整走过 expiry 才更新 `live[b]`，后续 bundle 列旧集合并省掉短身份，也不带其 tombstone。B04 [checkTargetGrants](../../controller/internal/releases/artifact.go) 对未提到的可能仍有效 grant 有此拒绝，和状态串联相符。没有原提交账本细节证明是哪一条行触发 guard；**最符合驱动状态串联及正常护栏**。不能将 CLI 拒绝算成 foreign 监听验证或错误释放端口。

两轮均在 REJECT 子用例之前退出，未覆盖对真实 REJECT 行为及已验证回退的断言。最小后续是分离端口冲突与 REJECT 测试及状态，使用已验证的旧 revision，记录候选和回退独立的失败阶段、原 Listener 健康、占用身份、上游不放行及账本端口状态；若确实恢复失败，再按现有 B05 restore 路径修最小范围 B04/B05。即便旧配置无法安全恢复，也必须保持真实 failed/pending、正确进程归属和端口占用。

## runtime_failclosed

**P1 保护行为待证，拒绝原因已有证据。** `topology-ubuntu2.json` 的 Alpine Agent 首先通过缺 key 分支。在错 key 分支，`ubuntu-runtime_failclosed-private-error.log` 停在 `run_cli` 的“退出码 1 且 stderr 含预期串”联合断言；同一时刻 `alpine/agent-run-1794883956981839470.log` 有精确 `CHECKPOINT_INVALID_OR_WRONG_KEY`。驱动把 stdout 与 stderr 拼接保存，只保留断言类型，缺实际退出码及流归属；对应预期串有证据，无法判断为何联合条件没成立。驱动没有到达对原 PID 和端口的检查，坏检查点、降级也未走到。前一回退失败可能影响运行前提，运行原因日志没有显示从此推出的错误。相反拓扑的 Ubuntu Agent 完整通过三项及降级检查。

退出失败并只停止经身份严格确认的旧内核是产品功能；仅有错误日志不能证明停机，也看不到持久状态丢失的实证。最小后续是在本地独立用例保留退出码和 stderr，并在恢复文件前明确核对归属 PID、端口及原检查点可在恢复后读回；若清理失败，检查 [composeRuntime 的错误分支与 Process.Stop](../../agent/internal/agent/runtime.go)。优先维持现有 fail-closed 策略。

## public_protocol_failure

**P2 单向失败，证据不足；该方向另有阳性。** `topology-ubuntu2.json` 在完成 DAG 后记录 Ubuntu 客户端 → Alpine 服务端的 SS2022 `AssertionError`，未生成该次 `public_protocol` 逐项记录，也未在现存本机文件找到该次 traceback 和 probe 输出。按当前驱动，逐项记录先于最终断言；如果该版本与当时一致，异常可能更早出现在 fixture ready、远程 helper 等环节。不能确定是正常 TCP/业务 UDP、错凭据、客户端端口或环境，也不能从异常推出认证绕过。时间顺序早于 expiry 等用例，无后续串联污染。该拓扑的反向 SS2022 通过，`alpine3` 对同一方向及反向均通过。

正常业务回显和明确拒绝的双断言合理。最小后续是仅复核该失败分项时保留 setup/ready、正常分项、拒绝及拆除各自的阶段结果。当前允许后续固定 IPv4 的隔离开发，不以此确认同一方向每次稳定。

## batch

**额外容量与分页可延期；实际出现的发布失败保留。** `topology-ubuntug.json`、时间线和 `ubuntuug-batch-private-error.log`：Ubuntu Controller + 同机 Ubuntu Agent，累积本机 TCP Listener。源码每次加入一个 Listener，前 20 个成功有里程碑，时间线共 22 次提交，第 22 次（target revision 23）终态 failed：Applied、verification failed、rollback confirmed。按顺序脚本，到达第 22 次意味着此前 21 个任务已完成成功检查。第一次到期目标还剩数分钟，失败发生在生成期，尚未进入 Controller 停止、106 条 outbox 或重放；既无积压通过，也无分页本身失败的证据。

可能是规模提升后重验负担、临时探测/端口或本机环境；未保留失败 Listener 和步骤。确认回退意味着 Controller 收到了所需回退状态，不能据此确认所有旧连接无间断；也无丢状态或错误释放的证据。最小后续先在模块范围验证较小增量的阶段诊断及独立的计数/字节限制；以后再作容量门槛，分别记录生成规模、实际线帧、持久事件与 receipt。若普通生产规模也出现相同失败，应提升优先级，不能以“压力测试”长期略过。

## 可并行开发与交付条件

- **B06** 可先做路径/出口的数据模型、图与引用校验、编译及隔离的固定 IPv4 正常路径。默认绑定、冲突恢复、共享占用释放及端到端正式发布，上述 P1 项未解前不能确认。
- **B07** 可做规则解析、配置生成、DNS 拓扑校验和 provider 契约，以固定输入和模拟已发布视图推进。真实三节点转发、DNS/出口策略验收仍依赖 B06 与可信的 B04/B05 发布回退。
- **B08** 可做订阅数据模型、token 与下载鉴权、凭据代次及 API 的独立实现。涉及 Agent 的到期/撤销、他人连接不中断、确认后复用、掉线后状态恢复必须依赖本轮 P0/P1 证据补齐，并保持 Pending 语义。产物越权与旧管理身份失效等未执行项仍是相关功能的验收项。
- 大容量独立事件、设备掉电、平台中断而未执行的非关键补充覆盖继续 pending；不挡上述独立开发。T03 真实集成和完整功能放行不使用 partial T02 替代。本轮没有派发后续任务。
