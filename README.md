# fynequery — TablePro 风格的 Fyne 数据库客户端

基于 **TablePlus** 官方界面截图（见 `design-ref/`）设计的数据库客户端，
使用 Go + Fyne（v2.8）构建。UI 结构与 TablePlus 一一对应。

**已接入真实数据库**：MySQL 为内置默认驱动；PostgreSQL / SQL Server / Oracle /
SQLite 通过独立 exe 插件进程提供，主程序启动时自动发现，无需重新编译。

## 架构

```
┌───────────────────────────── fynequery.exe ─────────────────────────────┐
│  UI (Fyne)                                                              │
│    welcome.go   欢迎页：连接列表（connections.json），点击真实拨号        │
│    dialog.go    新建连接：驱动列表来自注册表，Test/Connect 真实连接       │
│    workspace.go 工作区：表清单/数据网格/结构页，全部来自 Session          │
│    sidebar.go   侧栏树：Tables/Views 来自 Session.Tables()，支持搜索     │
│    sidebar_menu.go 侧栏右键菜单：复制脚本/仿制/清空/删除                │
│    details.go   右栏"详情"：选中行字段值，可点选编辑 + 快捷值菜单        │
│    query.go     SQL 编辑器：真实执行、耗时、错误弹窗、历史               │
│    complete.go  编辑器自动补全：关键字/函数/表/列（Ctrl+Space）          │
│    grid.go      数据网格（斑马纹/选中/NULL/右对齐数字）                  │
│                                                                          │
│  db/      数据层：Dialer/Session 接口 + 驱动注册表                        │
│    mysql.go        内置 MySQL/MariaDB（go-sql-driver/mysql，纯 Go）      │
│    pluginhost.go   插件宿主：发现/拉起/JSON-RPC 客户端/进程生命周期       │
│    (connstore.go)  连接模型 + 持久化（根包）                              │
└──────────────┬───────────────────────────────────────────────────────────┘
               │ stdin/stdout 按行 JSON（fynequery/proto 定义）
     ┌─────────┴──────────┬────────────────┬──────────────────┬──────────────┐
     │ fq-plugin-postgres │ fq-plugin-mssql│ fq-plugin-oracle │ fq-plugin-   │
     │ (jackc/pgx/v5)     │ (go-mssqldb)   │ (go-ora，纯Go)   │ sqlite       │
     │                    │                │                  │ (modernc.org/│
     │                    │                │                  │  sqlite，纯Go)│
     └────────────────────┴────────────────┴──────────────────┴──────────────┘
```

- **插件协议**（`proto/`）：子进程方式。插件启动输出 banner
  `{"proto":"fynequery-plugin","version":1,"code":...}`，宿主校验后按
  `{"id":N,"method":"meta|open|tables|columns|query|browse|close","params":{...}}`
  请求，插件逐条回 `{"id":N,"result":...}` 或 `{"id":N,"error":...}`。
  新增数据库类型 = 实现一个 `Dialect`（`plugins/internal/pluginkit`）+
  `go build` 出一个 `fq-plugin-<name>.exe` 放进 `plugins/` 目录，主程序
  零改动。
- **线程模型**：所有数据库调用在后台 goroutine，回调经 `fyne.Do` 回 UI 线程。
- **插件生命周期**：以 `CREATE_NO_WINDOW` 隐藏启动；stderr 重定向到日志；
  应用退出时统一回收（先关 stdin（优雅）后 kill）。

## 构建与运行

需要 Go 1.21+ 和 C 编译器（Windows 上为 MinGW-w64 gcc，Fyne 要求）。

```powershell
./build.ps1          # 产出 dist/fynequery.exe + dist/plugins/*.exe
./dist/fynequery.exe
```

开发期直接运行也可以（插件从工作目录 `./plugins` 发现，需先构建）：

```sh
go build -o fynequery.exe .
go build -o plugins/fq-plugin-postgres.exe ./plugins/postgres
go build -o plugins/fq-plugin-mssql.exe    ./plugins/mssql
go build -o plugins/fq-plugin-oracle.exe   ./plugins/oracle
go build -o plugins/fq-plugin-sqlite.exe   ./plugins/sqlite
./fynequery.exe
```

命令行参数：`-dark` 深色主题；`-conn` 直接打开第一个已保存连接（失败回欢迎页）。

## 使用

1. 欢迎页 → "Create a new connection..." → 选类型（MySQL/PostgreSQL/
   SQL Server/Oracle/SQLite，来自驱动注册表）→ 填 host/port/user/password/database
   → **Test** 验证连通或 **Connect** 保存并进入工作区。SQLite 的 Database
   填 `.db` 文件路径（或 `:memory:`）。
2. 连接保存在 `%AppData%\fynequery\connections.json`；右键连接行可删除。
3. 侧栏 Tables/Views 来自真实数据库；点击表加载前 1000 行（状态栏显示
   近似总行数 `~N`，来自 information_schema 统计）；Structure 页显示真实
   列与主键。左侧树项右键可 Open / View Structure / 复制名称、CREATE、
   SELECT、TRUNCATE、DROP 脚本 / Clone / Truncate / Delete。
4. 右侧"详情"栏显示**选中行**的各字段值：点字段值就地编辑（自动开启表
   编辑模式），点右侧箭头选快捷值（NULL / EMPTY / 日期时间列的 NOW()）；
   改动进入底部事务条，Commit 写回、Rollback 放弃。
5. Query 标签页执行任意 SQL：SELECT 出网格（上限 5000 行），DML/DDL 报
   受影响行数；错误弹窗显示驱动原始错误；侧栏 History 记录最近语句。
   `Ctrl+Space`（或工具栏 Complete）在光标处补全关键字、函数、表名与列名。
6. 日志（插件诊断等）在 `%AppData%\fynequery\fynequery.log`。

## 支持的数据库

| 数据库 | 形式 | 驱动 | 默认端口 |
| --- | --- | --- | --- |
| MySQL / MariaDB | 内置 | go-sql-driver/mysql | 3306 |
| PostgreSQL | 插件 `fq-plugin-postgres` | jackc/pgx/v5 | 5432 |
| SQL Server | 插件 `fq-plugin-mssql` | microsoft/go-mssqldb | 1433 |
| Oracle | 插件 `fq-plugin-oracle` | sijms/go-ora/v2（纯 Go，免装客户端） | 1521 |
| SQLite | 插件 `fq-plugin-sqlite` | modernc.org/sqlite（纯 Go，免 CGO） | — |

### 开发新插件

1. 新建 `plugins/<name>/main.go`，实现 `pluginkit.Dialect` 的 8 个方法
   （Code/Name/Version/DefaultPort/Open/ListTables/ListColumns/BrowseSQL）。
2. `go build -o dist/plugins/fq-plugin-<name>.exe ./plugins/<name>`。
3. 重启主程序，类型下拉里自动出现。

## 测试

```sh
go test ./...
```

- 根包：可拖动分隔、可编辑网格事务、侧栏右键脚本生成、右栏选中行详情、
  编辑器补全等单元测试。
- `proto`：协议消息与 serve 循环往返（含 NULL 单元格、错误路径）。
- `db`：插件宿主端到端——编译 `db/testdata/fakeplugin` 真 exe，走
  banner→meta→open→tables→columns→query→browse→close 全链路，含连接
  失败、未知方法、进程死亡等路径。不需要任何真实数据库。

## 主题

`theme.go` 实现 `tablePlusTheme`，明/暗两套调色板（取自参考截图取色）：

- **Light**：内容 `#FFFFFF`、侧栏 `#ECECEC`、工具栏 `#F7F7F7`、
  选中行 `#DDEBF7`、强调蓝 `#2F7BD9`
- **Dark**：内容 `#1E1E1E`、侧栏/工具栏 `#252525`、强调绿 `#007F3D`、
  选中行 `#094771`（配色来自对本地运行的 TablePlus 窗口像素采样）

切换：欢迎页 "Toggle Dark Theme" 按钮、工作区工具栏月亮按钮、`-dark` 参数。

## 说明与限制

- **密码明文存储**在 connections.json（本地开发工具惯例）；如需部署请自行
  换成 OS keyring 等方案。
- 行级编辑写回目前由内置 MySQL 驱动实现（`db.Updater` 可选接口）；插件驱动
  暂为只读，多行批量编辑、SSH 隧道、导出为后续计划。
- SQLite 连接不涉及主机/账号，Database 直接填文件路径；插件为纯 Go，免 CGO。
- 视觉回归截图在 `shots/`；`design-ref/` 是 TablePlus 官方截图参考；
  `.design-tools/` 是截图/像素采样辅助工具，不参与构建。
