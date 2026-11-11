export default {
  "OverviewPage": {
    "title": "概览",
    "goal": "一览工作区，查看待办并继续下一步操作。"
  },
  "createRoute": "创建路由",
  "statistics": {
    "label": "工作区总览",
    "nodes": "已注册节点",
    "routes": "已发布路由",
    "subscriptions": "有效订阅",
    "nodesHint": "已注册记录，不含已移除节点；连接状态单独计算。",
    "routesHint": "保留已发布版本，即使后续尝试失败也不会丢失。",
    "subscriptionsHint": "有效或即将到期且到期时间尚未来临。打开全部订阅可查看详情。"
  },
  "guide": {
    "stage": "开始使用 · 第 {value} 步，共 3 步",
    "nodes": {
      "title": "从添加节点开始",
      "description": "注册直连或反向演示节点，然后配置协议与端口。",
      "action": "添加节点"
    },
    "routes": {
      "title": "构建第一条路由",
      "description": "选择出口节点、检查路径并模拟发布。已保存的草稿显示在下方。",
      "action": "创建路由"
    },
    "subscriptions": {
      "title": "准备客户端配置",
      "description": "选择已发布路由、规则配置和到期时间，创建首个订阅。",
      "action": "创建订阅"
    }
  },
  "todos": {
    "title": "需要处理",
    "hint": "打开具体对象或任务，查看影响并继续。操作均为模拟。",
    "tasks": "全部任务",
    "all": "全部分类",
    "search": "搜索待办",
    "searchHint": "对象名称或稳定 ID",
    "category": "分类",
    "results": "共 {value} 项匹配",
    "noMatches": "没有匹配项",
    "noMatchesHint": "请尝试其他名称、ID 或分类。",
    "clear": "暂无待办",
    "clearHint": "当前工作区没有待办记录。这不代表网络健康状态已验证。",
    "openTask": "打开任务",
    "pagination": "待办分页",
    "revoke": "撤销待确认",
    "expiring": "检查到期时间",
    "categories": {
      "publish": "待发布草稿",
      "offline": "节点离线",
      "expiry": "到期 / 撤销",
      "rollback": "回滚待处理",
      "cleanup": "待清理",
      "sync": "待同步",
      "failure": "失败任务"
    }
  },
  "recent": {
    "title": "最近操作",
    "all": "全部事件",
    "empty": "暂无操作记录。请从上方建议的下一步开始。",
    "missing": "对象已不存在，历史记录仍然保留。",
    "context": "对象历史",
    "tasks": "最近任务"
  },
  "snapshots": {
    "title": "已保存的查询快照",
    "hint": "刷新前请选择一个对象。这里只读取现有演示记录，不探测节点或生成新测量。",
    "kind": "快照类型",
    "target": "单个对象范围",
    "choose": "选择节点、订阅或路由",
    "chooseHint": "请选择一个对象以查看已保存的快照并启用刷新。",
    "resources": "节点资源",
    "usage": "订阅用量",
    "measurements": "路由测量",
    "scope": "范围：{kind} · {name}。仅一个对象，不发起网络请求。",
    "noTargets": "此快照类型暂无可用对象。",
    "refresh": "刷新 · 模拟",
    "loading": "正在读取所选演示快照…",
    "read": "模拟读取完成。观测值与时间戳均未改变。",
    "cancelled": "模拟读取已取消，保留之前的快照。",
    "readAt": "上次本地读取：{time}",
    "failureHint": "模拟读取失败，仍显示之前保存的快照。可通过演示控制复现错误和离线场景。",
    "details": "查看详情",
    "bytes": "{value} B",
    "cpu": "CPU",
    "memory": "内存占用",
    "disk": "磁盘占用",
    "network": "网络速率快照",
    "percent": "{value}%",
    "perSecond": "{value}/s",
    "ms": "{value} ms",
    "revision": "版本 {value}",
    "samples": "{value} 个样本",
    "resourceStale": "已过期：超过工作区缓存时间（{hours} 小时）。",
    "sourceFailed": "源记录显示查询失败。下方数值来自保留的快照。",
    "resourceHint": "未知值表示尚未查询。速率快照不代表带宽限制或实时序列。",
    "noUsage": "未查询。此订阅没有已保存的用量快照。",
    "noMeasurements": "未查询。此路由没有已保存的测量记录。",
    "usageHint": "记录按范围和覆盖区间分别显示。不同范围、指标及重置周期不可相加。打开详情可查看更早的记录。",
    "cacheHint": "过期状态使用工作区缓存时间（{hours} 小时）。本地读取不会延长快照有效期。",
    "complete": "完整覆盖",
    "partial": "部分覆盖",
    "reset": "计数器已重置",
    "scopes": {
      "entry": "入口范围",
      "node": "节点范围",
      "tunnel": "隧道范围"
    },
    "metrics": {
      "cumulative_bytes": "累计流量",
      "connection_count": "连接数",
      "active_snapshot": "活跃连接快照",
      "unavailable": "不可用"
    },
    "measurementKinds": {
      "network_rtt": "网络 RTT",
      "browser_http": "浏览器 HTTP 耗时",
      "path_cost": "路径成本估算"
    }
  },
  "states": {
    "draft": "草稿",
    "validating": "校验中",
    "applying": "应用中",
    "verifying": "验证中",
    "published": "已发布",
    "failed": "失败",
    "rolling_back": "回滚中",
    "rolled_back": "已回滚",
    "rollback_pending": "待回滚",
    "rollback_failed": "回滚失败",
    "active": "有效",
    "expiring": "即将到期",
    "expired": "已到期",
    "revoked": "已撤销",
    "pending": "待处理",
    "synced": "已同步",
    "success": "模拟完成",
    "succeeded": "模拟完成",
    "confirmed": "已确认",
    "cancelled": "已取消",
    "pending_cleanup": "待清理"
  },
  "footer": "原型 · 演示数据。统计反映已保存的记录，不是实时网络扫描结果。",
  "controllerInfo": "设置与 Controller 信息"
}
