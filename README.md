# TablePro Fyne · 数据库查询客户端

基于 [TableProGo](../../README.md) 数据库逻辑实现的 **原生 GUI 数据库查询客户端**，
使用 [Fyne](https://fyne.io/)（纯 Go 桌面 UI）构建，不依赖 Webview / Wails 前端。

## 功能

- **多数据库支持**：MySQL、PostgreSQL、SQLite、SQL Server、Oracle
- **连接管理**：新建 / 编辑 / 测试 / 删除连接，密码使用 Windows DPAPI 加密保存
  （复用项目原有的 `connections.json` 与加密格式）
- **查询编辑器**：多行 SQL 输入，支持 `Ctrl+Enter` / `Cmd+Enter` 执行，
  关键字 / 库名 / 表名自动补全，可限制返回行数；
  支持多语句脚本（按引号/注释感知的方式切分，逐条执行、失败即停并报出序号）
- **结果表格**：动态列宽，表头显示列名与类型（点击排序），错误信息展示在状态栏；
  单元格栏内置「在结果中搜索」——输入片段即时高亮全部命中并定位首个，
  `Enter` 下一个、`Shift+Enter` 上一个（循环），命中计数 `n/N`，换结果自动清空
- **网格内编辑**（对照 TablePro）：`SELECT * 单表` 结果可直接编辑——
  编辑单元格（外键列提供引用表候选下拉）、插入行、删除行，保存前预览生成的
  UPDATE / INSERT / DELETE 语句，确认后在单个事务内提交（任一语句未命中
  行则整体回滚）
- **列过滤**：按列多条件 AND 过滤（等于 / 比较 / 包含 / IS NULL），
  生成的 WHERE 语句填入编辑器，透明可改
- **表操作**：结构树中右键表/视图 → 删除 (DROP) / 清空 (TRUNCATE) /
  重命名 (RENAME)，语句按方言渲染、执行前确认并记入历史
- **快速打开**：`Ctrl+P`（或结构页「搜索」按钮）输入表名片段，回车跨库搜索
  表/视图，点击结果自动切库、展开结构树、选中并填入 SELECT 预览
- **表结构查看**：结构树右键表/视图 →「查看结构」，字段（类型/可空/键/默认值/
  注释）、索引（类型/唯一性/字段）与 DDL（重构的 CREATE TABLE + 索引/外键，
  一键复制）分页展示
- **导出**：CSV / JSON / INSERT 脚本（.sql，需单表 SELECT *）/
  Excel（.xlsx，数值与日期为真实类型单元格，冻结表头、自适应列宽）
- **结构浏览**：左侧树形浏览 数据库 → 模式 → 表/视图（过滤框实时筛选），
  点击表自动生成 `SELECT * FROM …` 预览，右键可 浏览数据（直接出结果）/
  查看结构 / 删除 / 清空 / 重命名
- **行数上限**：100 / 500 / 1000 / 5000 / 无限制

## 项目结构

```
fynequery/
├── go.mod                  # 独立 Go 模块（避免与根 Wails 模块冲突）
├── main.go                 # 程序入口：创建窗口并运行
├── clientstore.go          # Client→Store 薄包装：连接生命周期、外键缓存、XLSX 落盘
├── settings.go             # 界面状态持久化（主题/窗口/分割位/标签草稿）
├── ui.go                   # Client 状态与主布局（侧栏+主区装配）
├── ui_connections.go       # 连接面板（下拉/连接/管理）
├── ui_connections_dialog.go# 连接新建/编辑对话框（SQLite 文件选择器）
├── ui_suggest.go           # SQL 补全控件：suggestEntry、词元定位、候选匹配
├── ui_editor.go            # SQL 编辑器编排：查询标签、行号槽、候选条、补全池
├── ui_grid.go              # 结果网格模块：单元格状态、列宽、排序、复制、导出
├── ui_grid_search.go       # 结果集内搜索：扫描命中、Enter/Shift+Enter 跳转、高亮
├── ui_grid_edit.go         # 网格编辑队列与操作
├── ui_query.go             # 查询页装配与执行入口（runCurrent）
├── ui_filter.go            # 过滤条件对话框
├── ui_search.go            # 快速打开对话框
├── ui_structure.go         # 表结构查看器
├── ui_schema.go            # 结构导航渲染：行构件按 uid+外观记忆化，集合未变跳过重排
└── internal/db/            # 数据层（抽象屏障：编译器强制，不含任何 fyne 依赖）
    ├── db.go               # Store 内核：连接表/连接池/语句历史的唯一持有者
    ├── core.go             # 数据类型与常量（ConnConfig/QueryResult/…）
    ├── store.go            # 连接持久化（加载/保存/增删改）
    ├── store_connect.go    # 打开/测试连接、按库缓存连接池、DSN 构造
    ├── query.go            # 查询执行（SELECT/命令）、多语句脚本、标识符引用
    ├── dialect.go          # 引擎方言操作表：各引擎目录 SQL 集中于此
    ├── browse.go           # 结构浏览：数据库/模式/表（方言表的 apply-generic）
    ├── browse_columns.go   # 列元数据、索引聚合
    ├── changes.go          # 网格编辑引擎：UPDATE/INSERT/DELETE 生成与事务提交
    ├── exportsql.go        # 导出 INSERT 脚本
    ├── export.go           # CSV/JSON 导出
    ├── exportxlsx.go       # Excel 工作簿渲染（excelize，纯字节输出）
    ├── filters.go          # 列过滤条件 → WHERE 渲染
    ├── tableactions.go     # 表操作（DROP/TRUNCATE/RENAME）
    ├── fk.go               # 外键目录读取与引用表候选值
    ├── search.go           # 跨库对象搜索（快速打开）
    ├── ddl.go              # 从目录行重构 CREATE TABLE
    ├── script.go           # 多语句脚本切分器
    ├── history.go          # 语句历史（记录/截断/持久化）
    ├── secrets_windows.go  # Windows DPAPI 密码加密（build:windows）
    └── secrets_other.go    # 非 Windows 平台明文存储（build:!windows）
```

分层规则：`internal/db` 只依赖标准库与驱动，UI 通过 `db.Store` 的导出方法访问
数据；界面控件、会话选择（当前连接/当前库）与各类缓存留在 `main`。新增引擎
只需在 `dialect.go` 的方言表中加一行。

## 构建与运行

要求：Go 1.22+、具备 CGO 的编译器（Windows 上可用 MinGW/TDM-gcc 等）。

```bash
cd fynequery
go mod tidy
go build -o fynequery.exe .
./fynequery.exe
```

## 使用说明

页面布局对照 TablePro Go：左侧单一侧栏——「导航」标题（＋新建连接）、连接
下拉与连接/断开、测试/编辑/删除、对象过滤框（实时筛选已加载对象）、结构树、
底部历史栏；主区为**多查询标签页**（每页独立编辑器，可新建/关闭，共享结果
网格），标签下工具栏为 数据库 → 执行（高亮）→ 快捷键提示 → 行数上限 → 导出，
网格底部为状态/操作工具条。

其他要点：

- **异步连接与查询**：连接握手、表目录加载、SQL 执行均在工作线程进行，界面不冻结；执行中可点「取消」中断长查询（fyne 2.6+ 的 fyne.Do 调度回主线程）
- **编辑器行号槽**：等宽 SQL 编辑器左侧带行号（禁用多行输入框实现，与编辑器同族同字号精确对齐）；长脚本在标签内滚动而非拉伸窗口，编辑时自动跟随光标行
- **查询标签**：多页独立编辑器（Ctrl+N 新建），修改未执行的标签标题带 ● 标记、关闭前确认；切换标签时补全候选随活动页切换；关闭时各标签的 SQL 草稿与脏标记写入 `ui.json`，重启后完整恢复
- **主题**：工具栏可切 跟随系统/浅色/深色（即时生效并持久化）

1. **新建连接**：在侧栏顶部选择器旁点击「新建」，填写类型、主机、端口、账号等。
2. **测试**：点击「测试」验证连接参数。
3. **连接**：在下拉框选中连接后点击「连接」，侧栏中部会加载该库的结构树。
4. **查询**：在右侧编辑器输入 SQL（有自动补全），点击「执行」或按 `Ctrl+Enter`。
5. **浏览结构**：在侧栏结构树点击表名生成 SELECT 预览；右键表可查看结构 /
   删除 / 清空 / 重命名；`Ctrl+P` 快速按名找表。
6. **编辑数据**：`SELECT * 单表` 结果可在网格底部工具条编辑单元格、插行、
   删行，确认后事务提交。

## 说明

- 本模块为独立子模块，与根目录的 Wails 版本（`TableProGo`）互不影响，
  可并存于同一仓库。连接文件路径与根版本一致，因此可共享已保存的连接。
- 所有数据库操作在当前线程同步执行；长查询期间界面会等待（初版取舍）。
