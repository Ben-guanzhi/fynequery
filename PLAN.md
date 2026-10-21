# fynequery 复刻 TablePlus 计划

基于页面差异分析（对照 `design-ref/` 下的 TablePlus 官方截图），按「视觉差距 × 改动成本」
排序，分 4 个阶段。所有改动都在现有 Fyne 结构内做外科式修改，不推翻既有骨架。

- 目标：让每个页面的信息密度与交互深度贴近 TablePlus。
- 参考基准截图：`design-ref/tp-page-data.png`（数据页 + 详情）、
  `design-ref/tp-live-workspace.png`（绿色 ribbon）、`design-ref/mac-welcome.png`（欢迎页）。
- 当前实现对照：`shots/mine-*.png`。

## 现状差异摘要

| 页面 | 已对齐 | 主要差距 |
| --- | --- | --- |
| 欢迎页 | 双栏、连接列表、类型徽标、tag | 分组不可折叠；Backup/Restore/License 是无动作小超链接 |
| 工作区顶栏 | 文档标签行、图标工具栏雏形 | **缺绿色服务器 ribbon**；信息被拆到两个 pill 与窗口标题 |
| 左侧栏 | 项目/查询/历史记录、搜索、函数/表格/视图树 | 底部是只读标签，无 schema 下拉、无 `+` |
| 数据页 | `< >` 导航、行号、NULL、类型着色、底部 数据/结构/索引 + 分页 | `筛选器`/`列`/`≡` 为空操作 |
| 详情面板 | 详情/助理 tab、字段搜索 | 单行 key→value，不显示类型、不可编辑 |
| 查询页 | 编辑器 + Run/Beautify + `#` 结果网格 | 标签为中文；无 `● Query result — N columns` 状态行 |

---

## 阶段 P0 — 补齐核心视觉差异（先做，收益最大）

| # | 任务 | 目标文件 | 具体做法 |
| --- | --- | --- | --- |
| 0.1 | **顶栏绿色服务器 ribbon** | `workspace.go:432` `buildStrip`、`widgets.go`、`theme.go` | 新增 `serverRibbon` widget，替换目前居中的 `connCrumb` pill（`workspace.go:436`）。文案 `LOCAL \| <driver> : TLSv1.2 : <server> : <db> : <table>`，圆角绿底白字 + 右侧黄色 info 图标。palette 加 `ribbonBG/ribbonFG/ribbonInfo`（暗色取 `#007F3D` 系）。点击各段落分别触发连接选择器 / 库选择器 |
| 0.2 | session 补 TLS 信息 | `db/driver.go`（Session 接口）、`db/mysql.go` | `Server()` 已有；TLS 无则 ribbon 显示占位，或从 `SHOW STATUS LIKE 'Ssl_cipher'` 取 |
| 0.3 | **详情面板两行卡片** | `details.go:41` `metaList` | 把 `kv{k,v}` 扩成 `{key, typ, val}`；每项渲染两行：`key(灰) + type(右灰)` / `值框 + 下拉箭头`（样式取 `tp-page-data.png` 右侧）。`fetchTableMeta`（`details.go:190`）增补列类型 |
| 0.4 | **侧栏 schema 下拉 + 加号** | `sidebar.go:251` | 把只读 `dbLabel` 换成 `widget.Select`（库列表来自 `listDatabases` `dbpicker.go:35`）+ `+` 按钮；`onSelect → workspace.switchDatabase`（已存在 `workspace.go:247`） |

## 阶段 P1 — 欢迎页与查询页对齐

| # | 任务 | 目标文件 | 具体做法 |
| --- | --- | --- | --- |
| 1.1 | 欢迎页**可折叠分组** | `welcome.go:103` `reload` | 组头加 ▶/▼ 点击折叠；行容器 `Show/Hide` + 重排 |
| 1.2 | Backup/Restore/License 做成**图标** | `welcome.go:59` | 三个圆形图标 + 文字，替换现小超链接；各接一个 info / 窗口 |
| 1.3 | 查询页标签与状态行 | `query.go:64`、`widgets.go:833` | `运行当前/美化` → `Run Current/Beautify`；`statusBar.setQueryInfo` 现为空实现（`widgets.go:833`），补上 `● Query result — N columns` 绿点行 |

## 阶段 P2 — 功能从「占位」变「可用」

| # | 任务 | 目标文件 | 具体做法 |
| --- | --- | --- | --- |
| 2.1 | **筛选器** | `workspace.go:522`（现空操作） | 弹窗 列 / 操作符 / 值 → 拼 `WHERE` → 走 `gotoOffset` 的查询路径重载 |
| 2.2 | **列**（显示 / 隐藏） | `workspace.go:523`、`grid.go` | 弹窗勾选列 → `dataTable` 增 `hiddenCols` 掩码，`columnWidths` / `cellRenderer` 跳过 |
| 2.3 | Import from URL | `welcome.go:82` | 解析 DSN → 复用 `showNewConnectionDialog` 预填 |
| 2.4 | Backup / Restore 窗口 | 新增 `backup.go` | 复用现有 `Query` 通道（`db.Session`），mysqldump 风格的简单导出 / 导入 |

## 阶段 P3 — 大改造（可选项，视需求）

| # | 任务 | 目标文件 | 具体做法 |
| --- | --- | --- | --- |
| 3.1 | **可编辑数据网格** | `grid.go`、`db/driver.go`、`db/mysql.go` | `cellView` 双击进编辑；行 dirty 标记；Session 加 `UpdateRow/InsertRow/DeleteRow`；底部加 提交 / 回滚 事务条 |
| 3.2 | 顶栏图标集扩充 | `workspace.go:449` | 按 `tp-shot.png` 增加 鉴权 / 加锁 / 编辑模式 图标（先接 info 占位） |
| 3.3 | License / 本地密码锁 | 新增 `license.go` + `LocalPasscode` | 参照 TablePlus 的本地口令锁 |

---

## 验证方式（每阶段）

```powershell
go build -o fynequery.exe .                 # 编译
go test ./proto/... ./db/...                # 协议 / 驱动回归
.\.design-tools\shot.ps1                    # 截图对比 design-ref/tp-*.png
```

## 建议顺序

`0.1 → 0.3 → 0.4 → 1.1 → 1.3`（一天量级的纯 UI 对齐，立竿见影），再推进 P2；
P3 视是否需要写回功能决定。

## 约束

- 本项目为 Go + Fyne（v2.8），Windows 目标；构建需 C 编译器（MinGW-w64 gcc）。
- 保持现有 `db.Driver` 注册表 + 插件进程架构不变；新增数据库类型仍走 `plugins/`。
- 不破坏 `go test ./proto/... ./db/...`（协议与插件宿主端到端测试）。
