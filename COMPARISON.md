# fynequery 与 banyp/db-dev (Tabula) 对照

> 目的：把本地 `fynequery`（纯 Fyne 多数据库客户端）与远端 `https://cnb.cool/banyp/db-dev`
> （Tabula，同作者的"类 TablePlus 多数据库客户端"）在 **UI 层 / 数据层 / 插件层** 三个维度逐项对照。
>
> 关键前提：远端 `db-dev` 实际有**两套 UI**——根 `main.go` 是 **Wails v3 + Web**（主产品，CodeMirror 前端），
> `cmd/fyne/main.go → ui.Run()` 是 **纯 Fyne 实现**（`internal/ui/`）。因此本对照里与 `fynequery` 直接对标的
> 是远端的 `internal/ui/`（同为 Fyne），而远端真正功能碾压本地的是其 **Wails 主产品**的其余部分。
>
> 结论先行：两者是同一作者的两个不同架构实现。本地 `fynequery` 把远端的 Fyne 原型
> （`internal/ui/`，已停留在"参考设计"期）重写成生产级；但在"功能广度"上，远端 Wails 主产品（AI/MCP/SSH/池/事务…）
> 大幅领先本地。

---

## 0. 顶层差异速览

| 维度 | 本地 `fynequery` | 远端 `db-dev` (Tabula) |
|---|---|---|
| 模块名 / Go 版本 | `fynequery` / go 1.27 | `tabula` / go 1.25 |
| GUI 框架 | 纯 Fyne v2.8 原生控件 | Wails v3 原生壳 + 内嵌 HTML/CSS/JS（CodeMirror 5）；另保留一套 Fyne 实现 `internal/ui/` |
| 驱动架构 | MySQL 内置 + PG/MSSQL/**Oracle**/SQLite 走**独立 exe 插件进程**（JSON-RPC） | 全部**进程内** `database/sql`（lib/pq/go-mssqldb/modernc sqlite/go-sql-driver/mysql）+ 兼容别名 |
| 引擎覆盖 | MySQL·PG·SQL Server·**Oracle**·SQLite（独有 Oracle） | SQLite·MySQL·PG·SQL Server + 大量兼容别名（MariaDB/TiDB/CockroachDB/Aurora/Redshift/Neon…；无 Oracle） |
| 代码规模 | ~22.4k 行 Go | ~20.3k 行 Go + 14 个前端文件 |
| 产品成熟度 | Fyne 路线生产级重写 | Wails 主产品功能齐全、商业化打磨；Fyne 路线（`internal/ui/`）为原型 |

---

## 1. UI 层：远端 `internal/ui/` vs 本地 `grid.go` / `workspace.go`

> 两边都是 Fyne，这是 apples-to-apples 对照。

### 1.1 结果网格（核心差异）

| 维度 | 远端 `internal/ui/resultgrid.go`（`ResultGrid`） | 本地 `grid.go`（`dataTable`） |
|---|---|---|
| 底层 | Fyne 原生 `widget.Table` | 完全自研 `widget.BaseWidget` |
| 虚拟化 | ❌ 一次性实例化所有单元格 `(行+1)×(列+1)` | ✅ 自研 `rowsWidget` + `rowView`/`cellView` 对象池，只渲染可视区+缓冲 |
| 大结果集 | 非虚拟化，卡/吃内存 | 流畅；`appendRows()` 支持流式分批增量绘制 |
| 列宽 | 固定 `SetColumnWidth(i+1,150)` | 自定义 `headerWidget`：拖拽改宽并冻结、双击复位、PK 列金色表头、数字右对齐 |
| 网格内搜索 | ❌ 无 | ✅ `setFindTerm`/`scanFindHits`/`stepFind` 暖色高亮 + `cur/total` 计数（Ctrl+F） |
| 单元格渲染 | 普通文本；NULL 斜体灰 | 按 SQL 类型着色、Query-Magics（`__format__`/`__goto__` 链接）、`fitCellText` 二分裁切防越界 |
| 选中模型 | 仅整行 `selectedRow` | 整行高亮 + 活动单元格焦点环（`selRow+selCol`） |
| 排序 | `sortBy` 数值/字符串比较 | `sortData` 额外 NULL 恒沉底、按声明类型判数值列 |
| 编辑 | 底部 `editBar` 改单格；提交逐行拼 UPDATE | 富得多：`dirty`/`deleted`/`inserted`、PK 定位、双击 `beginEdit`、`quickLook` 看大值 |
| 悬停/隐藏列 | ❌ | ✅ `hoverRow`、`setHidden` |
| 导出 | CSV/JSON/SQL 内置于网格 | `grid_copy.go` 专做复制/导出 |

### 1.2 主窗口与外壳

- **远端 `app.go`（954 行）**：单 `App` 结构，`buildUI()` 用 Fyne 原生容器一次摆好（顶栏胶囊 + 左侧对象树 + 中间编辑器/结果 + 右侧"详情/助理"占位 + 底部导出条），多结果用 `container.DocTabs`。原型式写法。
- **本地 `workspace.go`（1953 行）**：每连接一个窗口，由独立 welcome 页拉起；大量自研外壳——`chromeDragArea`+`enableCustomChrome`（无边框自定义标题栏）、`accentSplit`（可拖拽改变列宽的三栏）、`tabBar`、`panelBackground`、`segmented`、`ribbon` 连接条、`consolePane` 控制台日志、Ctrl+K 快速打开、Ctrl+F 网格搜索。

### 1.3 对象树 / 侧栏

- **远端**：Fyne `widget.Tree`，亮点在**异步懒加载**（goroutine 查 `TableListQuery`/`ColumnListQuery`，`cache`/`loadErr`/`loading` 三表管理，失败内联显示，搜索过滤）。只到 `conn → tables/views → columns` 三层，无右键菜单。
- **本地 `sidebar.go`**：自定义树，功能更全——数据库选择器、连接文件夹、右键菜单（复制脚本/仿制/清空/删除）、保存查询树、函数分组、项菜单。

### 1.4 SQL 编辑器与补全

- **远端 `autocomplete.go`（162 行）**：关键字 + 缓存表/视图名，前缀匹配，弹 `widget.List` 替换词。**只补表名，不补列名**。
- **本地 `complete.go`（25KB）**：as-you-type、关键字/函数/表/列、**`表名.` 列上下文感知**、Ctrl+Space。

### 1.5 主题

- **远端 `theme.go`**：`tabulaTheme` 实现 `fyne.Theme`，TablePlus 浅色 + 绿强调，**仅浅色**。
- **本地**：`theme.go` + `theme_json.go` + `pal()` 调色板，**支持深色模式**，调色板抽成 `palette` 供网格/侧栏复用。

**UI 层结论**：在 Fyne 路线上，本地已大幅超越远端 `internal/ui/`——网格、外壳、补全、主题都做了实质工程化。真正领先本地的是远端 Wails 主产品的广度（图表/ER/Vim/双栏等）。

---

## 2. 数据层：远端 `internal/db/` vs 本地 `db/`

### 2.1 总体架构

| | 远端 `internal/db/` | 本地 `db/` |
|---|---|---|
| 驱动运行 | 全部进程内 `database/sql`，编译进同一二进制 | 混合：MySQL 进程内；其余引擎是**独立 exe 插件进程**（`fq-plugin-*.exe`），经 `pluginhost.go` 走 `proto/` 的 stdio JSON-RPC |
| 隔离性 | 无进程隔离；一个驱动 panic 拖累 App | 每个插件独立进程（`CREATE_NO_WINDOW` 隐藏），崩溃隔离；退出由 `failAll` 统一唤醒等待者 |
| 扩展新引擎 | 写 `Driver` 实现 + `Register`（编译进二进制），或 `registerAlias` 复用线协议 | 丢 `fq-plugin-<name>.exe` 到 `plugins/`，`DiscoverPlugins` 自动注册，主程序零重编译 |
| 引擎覆盖 | SQLite/MySQL/PG/SQL Server + 兼容别名（无 Oracle） | MySQL + PG/SQL Server/**Oracle**/SQLite（独有 Oracle） |

### 2.2 驱动抽象

- **远端 `driver.go`**：`Driver` 接口很重——`Key/SQLDriver/DSN/QuoteIdent/Placeholder/DefaultColumnType/DefaultPort/ExplainSQL/TablesQuery/ViewsQuery/ListSchemasQuery/ColumnsQuery/SplitSchema/FormatColumnLabel` + `GetColumns/PrimaryKey/GetIndexes/GetForeignKeys/GetTriggers/GetDDL/GetDiagramData`。`ensureCompatAliases()` 用 `compatDriver` 包装复用底层驱动零依赖扩引擎。
- **本地 `driver.go`**：`Dialer`/`Session` 接口极简——`Session` 仅 `Server/Tables/Columns/Query/Browse/Close`，外加**可选** `Updater`/`BatchUpdater`/`DatabaseCreator`/`Canceller`（隐式实现，能力按需探测）。方言逻辑下沉到每个 Session/插件；`db/` 只通过 `proto.ConnectParams` 接收拨号参数，不碰持久化。

### 2.3 连接模型与凭据

- **远端 `connection.go`**：`Connection` 字段极全（SSH/SSL/Color/Env）；`Manager` 读写 `connections.json`，加载时把**明文密码迁出到系统密钥库**（`zalando/go-keyring`，回退 `secrets.json`），并修了 SQL Server 历史脏数据。
- **本地**：连接模型在根包 `connstore.go`，同样用系统密钥库（`keyring_windows.go`）；`db/` 层只接 `ConnectParams`。

### 2.4 查询执行

- **远端 `exec.go`**：`Run` 按首关键字分流 Query/Exec；`SplitStatements` 正确处理字符串/标识符/行注释/块注释/PG 美元引号，支持多语句。`Open(c)` 走连接池。
- **远端 `pool.go`**：**连接池**——按连接指纹复用 `*DBHandle`，引用计数 + LRU（上限 32），配置变更 `Evict` 标记 doomed。对标 TablePlus 常驻连接。本地无此池（MySQL 用 `sql.DB` 自带池；插件走子进程常驻）。
- **本地 `mysql.go`**：`Query` 把语句 pin 到专用连接（`db.Conn`）并记录 `CONNECTION_ID`，使 `KillQuery` 能精准 `KILL QUERY` 而**不打扰连接池**——本地独有"语句级取消"能力（远端靠 context cancel，无 KILL）。

### 2.5 编辑回写逻辑（集中式 vs 分布式）

- **远端 `edit.go`**：**集中式、同源自生成**。`EditRequest{Ops:[{Kind, Pk, Set}]}` → `BuildEditSQL` 生成 `EditPlan{Stmts:[PlanStmt{SQL,Args,Display}]}`——**执行 `SQL/Args` 与预览 `Display` 同源**，杜绝漂移；`ExecEditBatch` 在**一个事务内原子执行**，任一条 `RowsAffected != 1` 即 `ErrRowMismatch` 整批回滚，并对 SQLite busy 退避重试。UI 只拼 `EditRequest`，SQL 生成与事务兜底都在 `db` 包。
- **本地**：**分布式、下沉到每个 Session**。`db` 层只定义 `Updater{UpdateRow/DeleteRow/InsertRow}` 与 `BatchUpdater{ApplyChanges}`；具体实现在 `mysql.go`（`buildUpdate/buildDelete/buildInsert` + `ApplyChanges` 开事务）和**每个插件内部各写一份**。本地 `ApplyChanges` 也做事务原子提交，但**无"影响行数=1"强校验**，也无 SQLite busy 重试。另有 `DefaultMarker` 哨兵支持 TablePlus「Set → DEFAULT」语义。

### 2.6 本地 `db/` 缺失、远端 `internal/db/` 独有

远端数据层是完整后端，本地只覆盖基础部分。远端独有（文件级）：

- `tunnel.go` —— **SSH 隧道**本地端口转发
- `pool.go` —— **连接池**（指纹复用 + LRU）
- `tx.go` —— **手动事务模式**（10 分钟超时、提前告警、并发上限 8、关标签自动回滚）
- `stream.go` —— **流式结果**（`RunStream` 按批 200 行回调）
- `metrics.go` —— 只读聚合服务端指标
- `magics.go` —— **Query Magics**（列名后缀 `__link`/`__money`/`__enum` 等智能渲染）
- `review.go`/`resolve.go`/`search.go`/`diagram.go`/`import.go`/`export.go`/`dump.go`/`explain.go`/`params.go`/`schema.go`/`structure.go`/`index.go`/`alter.go`/`ddl.go`/`functions.go`/`favorites.go`/`aihistory.go`/`secret.go`/`sqlbuild.go` —— 整库搜索、ER 图、导入推断、导出 Excel/CSV/JSON/SQL、备份转储、EXPLAIN、命名参数、Schema 摘要等。

### 2.7 本地独有

- 进程隔离插件模型（安全/崩溃隔离、零重编译扩引擎）
- **Oracle 支持**（go-ora 插件）
- 语句级 `KillQuery` 取消

**数据层结论**：本地 `db/` 远不如远端 `internal/db` 完备（无 SSH/池/事务/流式/指标/导入导出等），但在**架构隔离性、扩展零成本、Oracle、语句取消**上独有。编辑回写远端靠集中式同源自生成保证全引擎一致；本地靠每个驱动自带 DML，但要保证各实现一致更费劲。

---

## 3. 插件机制：本地 `proto/` + `pluginhost.go` vs 远端 `internal/db/plugins/` + `internal/plugin/`

> "plugin" 在两边是**三个完全不同的概念**。

| 概念 | 本地 `fynequery` | 远端 `db-dev` |
|---|---|---|
| ① 数据库驱动插件 | ✅ **进程隔离的驱动**（`fq-plugin-*.exe`，JSON-RPC over stdio） | ❌ 无 |
| ② 驱动编译开关 | ❌ 无（驱动靠"丢 exe"运行时发现） | ✅ `internal/db/plugins/` = 空白导入 + build tag（`no_sqlite` 等） |
| ③ 应用扩展插件 | ❌ 无 | ✅ `internal/plugin/` = **JS 前端插件** + 白名单策略 |

### 3.1 本地：进程隔离驱动插件

- **协议**（`proto/messages.go`）：换行 JSON 请求/响应，子进程先写 `Banner{proto,version,code,name}`，宿主校验 `ProtocolName/Version`。方法 `meta/open/close/tables/columns/query/browse/createDatabase`。`QueryResult.Rows` 用 `[][]*string`（JSON null = SQL NULL），带 `DurationMS`。`messages.go` 顶部明确"改动须向后兼容已部署插件"。
- **宿主**（`pluginhost.go`）：`DiscoverPlugins` 扫描 `plugins/`（含工作目录回退）→ `spawnPlugin` 用 `exec.Command` + `hideWindow` 拉起 → `handshake` 读 banner + `meta` → `readLoop` 按 `id` 分发。`kill()` 先关 stdin（优雅）再 `Process.Kill()`。
- **插件侧**（`plugins/internal/pluginkit` + 各 `plugins/<engine>/main.go`）：实现 `proto.Handler` 八方法，编译成 exe。新增引擎 = 写 `Dialect` + `go build` 出 exe，主程序零改动。

### 3.2 远端 ②：`internal/db/plugins/` 只是编译开关

```go
//go:build !no_sqlite
package plugins
import _ "tabula/internal/db/drivers/sqlite"
```

空白导入触发驱动 `init()` 注册进全局表，配 `-tags no_sqlite` 等排除引擎。真正驱动在 `internal/db/drivers/{postgres,sqlite,sqlserver}`，全进程内。**无运行时发现/加载机制**——与本地"运行时丢 exe 即生效"相反。

### 3.3 远端 ③：`internal/plugin/` = JS 前端扩展插件 + 安全策略

- 插件是 `~/.tabula/plugins/*.js`，在 webview 前端用受限 `Tabula` API 执行，可注册命令面板条目/菜单项、调后端 SQL。对标 TablePlus JS 插件模型。
- **白名单策略**（`policy.go`）：`Policy{Mode:"all"|"allowlist", Allow:[]}`。`all`=未禁用即允许（出厂默认）；`allowlist`=**默认拒绝**，仅名单内可加载；管理员 `DisablePlugins` 优先级最高，一键全禁。`sanitize` 只取基名防路径穿越。

### 3.4 重要能力不对称（编辑回写 × 插件）

本地 `proto.Handler` **没有** update/insert/delete 方法（`grep` 确认），插件只实现八方法。因此经插件连接的 PG/MSSQL/Oracle/SQLite 网格**当前只读**——只有内置 MySQL（`mysqlSession` 实现 `Updater`/`BatchUpdater`）能编辑。

> 远端因编辑逻辑集中在 `internal/db/edit.go`（靠 `Driver.QuoteIdent` 适配方言），**所有进程内驱动都能网格编辑**；本地因编辑下沉到 `Session` 且协议未含 DML，扩来的引擎暂不能编辑。这是两种架构各自的核心权衡。

### 3.5 安全模型

- 本地：安全来自**进程边界**（插件崩/作恶不影响主程序；`failAll` 回收；exe 须带 `fq-plugin-` 前缀才被识别）。
- 远端：安全来自**前端沙箱 + 白名单治理**（JS 无外部网络、受控 API；`allowlist` 默认拒绝；管理员可全禁）。驱动侧无独立安全模型。

---

## 4. 总对照结论

| 层 | 本地 `fynequery` 强在哪 | 远端 `db-dev` 强在哪 |
|---|---|---|
| UI | 自研虚拟网格、无边框外壳、深色模式、网格搜索 | 功能广度（图表/ER/Vim/双栏）；Wails 主产品更完整 |
| 数据 | 进程隔离、零重编译扩引擎、**Oracle**、语句级 KillQuery | SSH 隧道、连接池、手动事务、流式、指标、导入导出/备份、集中式同源编辑（全引擎可编辑） |
| 插件 | 运行时进程隔离驱动插件（架构干净） | 编译期驱动开关 + JS 应用扩展插件 + 白名单治理 |

**一句话总结**：本地把"数据库引擎"做成可热插拔、隔离的外部进程（含 Oracle），为此牺牲了网格编辑的统一覆盖；远端把功能做成集中式进程内后端（SSH/池/事务/流式/全引擎编辑齐全），并用 JS 插件 + 白名单扩展 App 行为。两者在"如何扩引擎"和"如何扩 App"上恰好走了相反的路。

> 附：远端 `internal/ui/` 是 Fyne 原型（停留在参考设计期）；远端真正功能领先本地的是其 Wails 主产品。本对照中"UI 层"直接对标 `internal/ui/`，"数据层/插件层"对标远端整体数据后端。
