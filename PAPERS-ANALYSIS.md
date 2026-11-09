# 用三篇数据库经典文献分析 fynequery

> 参照文献：
> 1. **《Architecture of a Database System》** — Hellerstein / Stonebraker / Hamilton, FnTDB 2007（下称 **fntdb07**）
> 2. **《How to Architect a Query Compiler, Revisited》** — Tahboub / Essertel / Rompf, SIGMOD 2018（下称 **tahboub18**）
> 3. **《Readings in Database Systems, 5th Edition》**（红皮书）— Bailis / Hellerstein / Stonebraker, 2015
>
> 代码事实均带 `file:line`，来自 2026-11-03 的代码探索（fynequery + 兄弟内核 dbcore）。

## 0. 方法论：三篇论文各提供一面镜子

fynequery 是 **DBMS 的客户端**（TablePlus 风格 GUI），不是 DBMS 本体。三篇文献讲的几乎都是 DBMS
内部，所以正确的分析姿势不是硬套，而是三面镜子：

| 文献 | 视角 | 用它看 fynequery 的什么 |
| --- | --- | --- |
| fntdb07 | DBMS 五层解剖图（进程模型→并行架构→事务存储→查询处理→共享组件） | 逐层对照：哪些层被**外包**给服务器、哪些层被客户端**镜像**、哪些论文点名的必备件**缺失** |
| tahboub18 | 查询编译器架构（Futamura 投影、解释器=编译器、data-centric 回调、单遍 vs 多遍生成） | 客户端对 SQL 的"理解深度"标尺：fynequery 里真实存在的"编译器成分"（SQL 生成器、staging、单遍同源生成） |
| 红皮书 5th | 经典 canon + 现代议题（one-size-fits-all、End of Architectural Era、ARIES、OCC、MapReduce 之争、NewSQL） | 定位与批判：这个工具站在数据库演化史的哪个位置、哪些设计是"one size"的活案例 |

一个重要背景事实：红皮书第五版的**开篇第一篇就是 fntdb07**。所以第 1 节的逐层对照本身就是红皮书的主线；
红皮书其余文献（Gray 的事务概念、Kung-Robinson OCC、ARIES、Stonebraker 的 one-size-fits-all 系列）
在第 3 节作为批判性透镜。

---

## 1. 对照 fntdb07：五层逐层映射

论文把 DBMS 拆成：§2 进程模型与内存管理、§3 并行与分布式架构、§4 查询处理、§5/§6 事务存储
（缓冲池 / 锁 / 日志）、§7 共享组件与工具。

### 1.1 进程模型与内存管理（论文 §2）—— fynequery 实现得最"论文"的一层

论文 §2.1 列出四种 worker 模型：process per worker / thread per worker / process pool / thread pool；
§2.4 强调 **Admission Control** 是多用户 DBMS 的必备件。

fynequery 恰好在自己的边界内把这四种模型都玩了一遍：

| 论文概念 | fynequery 对应物 | 证据 |
| --- | --- | --- |
| process per worker（强隔离） | 每个非 MySQL 引擎一个 `fq-plugin-*.exe` 子进程；崩溃隔离、stderr 重定向、优雅关闭链（礼貌 close→关 stdin→failAll→Kill→Wait） | `db/pluginhost.go:177-267`；内核 `dbcore/plugindriver/driver.go:341-385`、`failAll` kernel:240-258 |
| process pool / 复用 | **一个插件进程服务多个连接、所有 UI tab 共享**；死进程惰性重拉（`ensure` 双检查自愈） | `db/pluginhost.go:4-10, 238-267` |
| thread per worker | 每请求一个 goroutine；插件内核 readLoop 按 `Response.ID` 分发 pending/stream；内核侧每请求独立 goroutine 服务（因此 `cancel` 能与在途 query 并行） | kernel `serve.go:100-121`；宿主 `pluginhost.go` readLoop |
| 单线程串行点（论文的 latch 思想） | 所有 stdout 帧写经 `writeMu` 串行；UI 回调经 `fyne.Do` 回主线程（Fyne overlay 不许离开主线程） | kernel `serve.go:58-72`；全项目 `safeDo`/`safeGo`（`crash.go:32-54`） |
| worker 身份可观测 | MySQL 查询 **pin 专有池内连接并记录 `CONNECTION_ID`**，`KillQuery` 用独立连接发 `KILL QUERY <id>`，结束后清零防误杀复用连接的陈旧线程 | `db/mysql.go:236-288` |
| Admission Control（§2.4） | **缺失**。全仓库无 `SetMaxOpenConns/SetMaxIdleConns`；`SessionPool` 只限空闲（maxIdle=16、idleTTL=10min、全局 LRU 逐出），在用会话不设上限 | grep 0 命中；`db/pool.go:25-28, 69-173` |

**评价**：进程隔离插件模型是 fynequery 相对同类（全进程内驱动）最大的架构亮点——它把论文 §2.1.4
"Shared Data and Process Boundaries" 的取舍推到了客户端驱动层：用进程边界换崩溃隔离与零重编译扩引擎
（丢 exe 即生效）。而 admission control 的完全外包是明确的短板：过载时客户端没有任何背压，论文 §2.4
的观点恰是"宁可排队也不让系统进入抖动状态"——对一个会同时开多 tab、多连接、3 秒指标轮询、30 秒
keepalive 的 GUI，这是最值得补的一课（见 §4 建议 1）。

### 1.2 并行与分布式（论文 §3）—— 单机客户端，只取通信层

不涉及 shared-nothing / 2PC / 复制：每个工作区窗口 = 一个服务器会话。但有两处工程可对号：

- **SSH 隧道** = 论文 client-server 通信层的客户端实现：`net.Listen("tcp","127.0.0.1:0")` 随机端口
  + 每连接双 goroutine `io.Copy` 转发；驱动完全无感知（`ConnectParams.Host/Port` 被改写），
  会话指纹把 SSH 配置计入防串会话。`db/ssh.go:51-144`、`connstore.go:100-114`。
  （HostKey 校验是 `InsecureIgnoreHostKey` + TODO——论文意义上的安全通信还差 known_hosts。）
- **协议工程** = 论文强调的 client/server 协议设计要点全部在场：版本化 banner 握手（5s 超时）、
  旧协议名向后兼容、方法集只增不改（`meta/open/…/queryStream/metrics/cancel` 增量）、
  "改动须向后兼容已部署插件"写进注释。`proto/messages.go`、`proto/kernel.go:32-38`。

### 1.3 查询处理（论文 §4）—— "客户端只做分类扫描，不做解析"的自觉

论文管线：4.1 解析与授权 → 4.2 改写 → 4.3 优化器 → 4.4 执行器（迭代子模型）→ 4.5 访问方法。

fynequery 的激进选择是**不解析 SQL**。客户端全部"SQL 理解"都是词法级分类扫描器：

| 客户端扫描器 | 用途 | 证据 |
| --- | --- | --- |
| `sqlkit.IsRowReturning` | 首词 ∈ {SELECT,WITH,SHOW,DESC,…} → QueryContext，否则 ExecContext | `internal/sqlkit/sqlkit.go:24-45` |
| `safe_gate.isReadOnlySQL` | Safe Mode 门禁的前缀白名单 | `safe_gate.go:51-62` |
| `autoLimit` | 客户端改写：裸 SELECT/可读 CTE 且无 LIMIT 时注入 `\nLIMIT n`（仅 mysql/pg/sqlite；跳过 FOR UPDATE/OUTFILE/TOP；CTE 判定调内核扫描器 `sqltext.CteOuterIsRead`） | `query_params.go:267-299` |
| `topLevelStatementRanges` | 引号/行注释/块注释感知的分号切分（Run All / Run Current） | `query.go:929-1015` |
| `tokenizeSQL`（sqlformat） | 6 类 token 的 best-effort 美化器，自述 "not a full parser" | `sqlformat.go:59-132` |
| 补全 `referencedTables` | 只认与已知表名**精确相等**的 token，无别名绑定 | `complete.go:168-185` |

论文 4.2 的 **rewriter** 在客户端唯一的存在就是 autoLimit——一个保守、白名单化的语句改写器；
4.3 优化器完全外包（正确：代价模型需要统计与目录，客户端没有）；4.4 执行器的客户端对应物是
**拉模型的消费端**：`StreamRun` 游标按批（默认 200，UI 层 500）emit → `appendRows` 增量上屏，
行数到 `maxRows` 即 break（`sqlkit.go:180-229`、`query.go:744-780`）——这是迭代子 `next()` 的
批量化版本。表头点击排序是纯客户端内存排序（`grid.go:184-260`，NULL 恒沉底），即放弃服务端
索引与 ORDER BY、用内存换交互——论文 cost 视角下的明确取舍（翻页才重新发 SQL）。

一个论文视角下的缺口：**不支持多结果集**（全仓库无 `NextResultSet`），Run All 只保留最后一条
语句的结果，用户须手动 Pin 快照（`query.go:279-350`）——迭代子模型的"多个活动游标并存"在 UI
层被简化为单槽 + 手动拍照。

### 1.4 事务存储（论文 §5/§6：缓冲池、锁、日志）—— 三大件全外包，但客户端各有镜像

| 论文组件 | fynequery 客户端镜像 |
| --- | --- |
| 缓冲池（§5.3 有界缓冲、按需换入） | **虚拟网格**：`rowsWidget` 对象池 + 可视区 ±6 行缓冲（`virtualBuffer=6`），滚动时池行"原地换数据"零重建；数据流式分批进入；各路径行上限 5000（查询）/300（浏览）/100000（备份） | `grid.go:31-37, 1111-1219`、`query.go:33`、`workspace.go:29-30`、`backup.go:29` |
| 锁管理器（§6.3） | **无**。并发写冲突靠 `RowsAffected == 1` 强校验：`applyWithin` 任一条 UPDATE/DELETE/INSERT 影响行数 ≠1 → `ErrRowMismatch` → 整批回滚 | `db/mysql.go:541-590`（插件侧同构 `plugindriver/dml.go:146-194`，SQLite busy 另有 5 次退避重试） |
| 日志管理器（§6.4 WAL） | **无客户端持久化**。未提交编辑（dirty/deleted/inserted + pendingDDL）纯内存，崩溃即丢（`crash.go` 只做 panic 取证不落盘）；关窗丢弃、**翻页直接 newDataTable 连编辑一起丢** | `edit.go:205-244`、`workspace.go:2157-2192` |
| 事务句柄交给用户 | 手动事务模式：`Transactional` 能力接口（Begin/Commit/Rollback/InTransaction），打开后 Query/Browse/DML 全走事务连接，Close 自动回滚残留 | `db/driver.go:106-111`、`db/mysql.go:214-235, 352-363`、`workspace.go:682-771` |

**评价**：编辑写回 = 一个**客户端 mini-ACID**：原子性（单事务提交）与"恰好 1 行"一致性校验在客户端做，
隔离与持久性诚实外包。`RowsAffected==1` 实质是**基于更新计数的乐观并发控制**（红皮书 Kung-Robinson
OCC 的精神：检测而非预防，冲突即整批回滚）。持久性是唯一没有客户端镜像的支柱——用户编辑了 20 格
还没点 Commit，窗口崩溃全部蒸发，这是论文标准下的诚实缺口（见 §4 建议 3）。

无 PK 表直接禁编辑（`edit.go:45-55`）而非退化到 rowid 定位——把"行的恒等标识"作为能力前提，
与论文"access method 需要稳定物理定位"的立场一致，代价是 Oracle/PG 的 ROWID 场景不可编辑。

### 1.5 共享组件与工具（论文 §7）—— fynequery 的本体

论文 §7.1 Catalog Manager、§7.5 Administration/Monitoring/Utilities。

- **目录管理器的客户端缓存**：`invCache`（表/视图/函数清单）+ `invVersion` 代际 + `colCache`
  （singleflight 防击穿）+ `sugCache`（按 invVersion/colVersion/dbName 三元组失效）；
  换会话即 `invCache=nil`（"旧会话的目录对新会话无意义"）。近似行数直接读
  `information_schema.TABLES.TABLE_ROWS`。`workspace.go:119, 920-970, 2423-2427`、
  `sidebar.go:53-188`、`complete.go:312-411`、`dbcore/mysql/mysql.go:466-470`。
  论文说 catalog 是 DBMS 的大脑；客户端做的是它的**投影缓存**（弱一致、显式失效）——这是正确的
  客户端姿势，尽管失效策略是"事件驱动重建"而非 TTL。
- **Utilities（§7.5）就是产品本体**。论文原话精神：商业 DBMS 的 utilities 代码量远超核心。
  fynequery 的功能表几乎逐条对应论文的 utilities 清单：

| fntdb07 §7.5 utilities | fynequery |
| --- | --- |
| 备份/恢复 | `backup.go`：mysqldump 风格（SHOW CREATE TABLE + 批 200 行多值 INSERT + SET FOREIGN_KEY_CHECKS 开关），10 万行 cap 打满写 WARNING；恢复走 DELIMITER 感知切分逐条执行（无事务包裹） |
| 监控/指标 | `metrics_ui.go`：`Metricser` 能力接口，PG 读 pg_stat_activity/pg_stat_database，SQLite 读页统计；3 秒 ticker + atomic 在途守卫防协程堆叠 |
| 管理（用户/授权） | `user_mgmt.go`：MySQL `mysql.user` / PG `pg_roles`，CREATE/DROP/GRANT，全部过 Safe Mode 门 |
| 批量加载 | `dataio.go`：CSV/JSON 导入（空字段→SQL NULL），`BatchUpdater.ApplyChanges` **单事务**原子写入；导出当前已取回行（2000 上限） |
| 结构工具 | ER 图（每方言一条 FK 目录查询，SQLite 退化到逐表 PRAGMA）、结构页、ALTER 生成（入 pendingDDL 队列与数据编辑同一提交条） |
| 诊断 | `dberror` 三级错误分类（context → SQLSTATE → 方言措辞词表），`friendlyErr` 前缀中文可行动提示、原文永附 |

---

## 2. 对照 tahboub18：查询编译器视角

论文三句话：① Futamura 第一投影——**编译器 = 解释器对程序的特化**，写好解释器就有编译器；
② 用该透镜审视执行器架构：pull（Volcano）/push（data-centric），并提出 **data-centric 回调模型**；
③ LB2 用**单遍高级语言生成**达到 DBLAB（5 种中间语言、多遍编译）与 HyPer（LLVM 低层 API）的
TPC-H 性能——复杂的多 IR 多遍管线不是高性能的必要条件。

### 2.1 fynequery 里真实存在的"编译器"

fynequery 不编译查询，但它包含一个货真价实的**编译器逆过程——SQL 生成器**：

- **IR = 内存变更集**：`pending()` 快照（updates=脏格+PK、deletes、inserts、DDL）。
  `edit.go:205-244`
- **codegen = 方言表驱动的单遍生成**：`buildUpdate/buildDelete/buildInsert`（`db/mysql.go:598-650`）
  + 内核 `sqltext.QuoteIdent/QuotedLiteral` 方言表（MySQL 反引号+反斜杠、MSSQL `[]`+`N'…'`、
  其余 `""`+`''`；`dbcore/sqltext/dialect.go:83-118`）。内核 sqltext 的包注释自我定位与论文的
  简单性哲学完全同频："every function maps strings to strings"——零依赖、字符串进字符串出。
- **两个 backend**：执行路径编译到 `?` + 绑定参数，预览路径编译到内联字面量（`sqlLiteral`）。
  这正是论文"同一 IR 可特化到不同 backend（C/LLVM/ASM）"的微型版。
- **单遍同源生成的一个活案例**：预览与执行共享同一 `pending()` 快照与同一 builder 家族
  （`edit_preview.go:84-127`，预览框的 Commit 按钮直接调 `ws.commitEdits()` 与事务条同入口）。
  唯一的文本分歧：INSERT 的 DEFAULT 哨兵——**预览显示 `DEFAULT` 关键字，执行却省略该列**
  （`edit_preview.go:32-34` vs `db/mysql.go:637-639`；语义等价、文本不同）。这正是论文反对
  "多遍生成漂移"的教科书案例：只要存在第二条生成路径，文本就会分叉。修复成本一行（预览
  渲染改为"省略该列"），价值是让"所见 SQL 即所执行 SQL"严格成立（见 §4 建议 5）。

### 2.2 staging（阶段化）思想的 UI 版

论文的 staging = 把昂贵计算划分为 offline（一次）/online（每查询）两阶段。fynequery 的缓存体系
是同构的低配版：目录清单（offline：loadInventory）、列快照（colCache + singleflight）、
候选列表（sugCache 按版本失效）、列元数据预计算（colMeta）、列宽缓存（cwCache）、文本裁剪
二分缓存。每一层都是"昂贵、不变的东西算一次，每次按键只做特化"。连竞态防护都是论文式的：
`runGen`/`autoGen`/`dbSwitchSeq`/`invVersion` 代际号守卫（`query.go:80, 684-699` 等）。

### 2.3 data-centric 回调的"精神同构"

论文 §3.1 的 data-centric 模型：数据推过固定代码，而不是代码拉数据。fynequery 的渲染管线恰好
是这个形状：`StreamRun` 的 batch 回调把数据**推**给网格，`rowView` 对象池是固定代码，
`bindIndex` 只换数据指针零 widget 重建（`grid.go:1253-1268`）。这是类比不是同源（真正的
data-centric codegen 是 Neumann 式编译管线），但架构直觉一致：**把变化的数据推给不变的代码**，
而不是每行每格重建对象。

### 2.4 用论文的标尺量"SQL 理解深度"，并指出真实的技术债

客户端刻意停在"词法分类"层是**正确边界**（见 §1.3）。但当前有**四套独立扫描器 + 两个切分器**
各写各的：`sqlkit.IsRowReturning`、`safe_gate.isReadOnlySQL`、`query_params` 的
stripSQLNoise/isPlainSelect/limitRe、`sqlformat.tokenizeSQL`，切分则分 `query.go` 的
topLevelStatementRanges 与 `backup.go` 的 DELIMITER 变体——后者**不跳块注释**，代码注释自己
承认"太有损"、只敢用在恢复路径（`query.go:924-928`）。两处对"同一 SQL"的判断已经出现
一致性与维护性风险。tahboub18 的核心论点（用一个高级语言、一遍生成，胜过 5 种 IR 多遍管线）
在这里的投影是：**扫描器应收敛到内核 sqltext 一处**（它已有 CteOuterIsRead/ownLimitKind/
skipLeadingTrivia 的一半家底），UI 各处只消费它（见 §4 建议 4）。

---

## 3. 对照红皮书 5th ed：定位与批判

红皮书 = 经典 canon（开篇即 fntdb07，另有 Selinger 优化器、Gray 事务概念、Kung-Robinson OCC、
ARIES 等）+ 现代议题（one-size-fits-all、End of an Architectural Era、Aurora、C-Store、
MapReduce 之争、NewSQL）。逐个透镜：

- **Gray《The Transaction Concept》+ Kung-Robinson OCC**：提交门控（`● N unsaved changes`
  事务条 → 单事务 ApplyChanges → 行数校验）是 OCC 的客户端实践——先改后验、冲突整批回滚，
  靠服务器行数反馈检测而非客户端持锁。选择正确：GUI 不能持长事务锁。
- **ARIES（恢复）**：客户端无 redo/undo 日志。未提交编辑不落盘、崩溃即丢、翻页即丢
  （§1.4）——在"本地工具"语境下可辩护，但 ARIES 的教训（先写日志再改状态）指出廉价改进：
  pending 变更追加写本地 `jsonl`，启动时提示恢复（见 §4 建议 3）。
- **One Size Fits All（Stonebraker/Çetintemel）**：通用 RDBMS 一统天下结束。fynequery 的
  多引擎架构是这个命题的客户端注脚——但最有意思的是它**同时呈现了命题的正反两面**：
  - 正面：方言差异全部下沉（QuoteIdent/QuotedLiteral/占位符 `$1 ? @p1` / 分页尾巴
    LIMIT-OFFSET vs MSSQL `OFFSET…FETCH` vs Oracle、autoLimit 方言白名单），新增引擎 =
    实现 8 方法 + 丢 exe。
  - 反面（活案例）：**Redis 被硬套进"表/列/SQL"通用抽象**——key 当表（SCAN 清单）、按类型
    合成列、`Query` 是"逐行执行换行分隔的原始命令、把回复拼成单列 reply 表"
    （`db/redis.go:136-148, 583-693`）。为此长出一串可选能力接口补丁：`ValueWriter/ValuePager/
    KeyAdmin/ValueDeleter/KeyPager/KeyFilter`（`db/driver.go:145-216`）。这正是
    "one size fits all"在客户端的失败形态：通用接口为特例长出十几个逃生舱口。方向正确的
    现有解法是有界读取（`GetValueBounded` 防百万成员/半 GB string）与游标分页——彻底解法是
    红皮书式的"按负载重写"：KV 专用 UI 模式，而非表形伪装。
- **End of an Architectural Era（H-Store）**：该文主张按负载彻底重写引擎。fynequery 对
  Redis 的特化接口路线与对 PG/MSSQL/Oracle/SQLite 的统一协议路线并存，恰好是两种哲学的
  同居。
- **MapReduce vs 并行 DB**：fynequery 明确选择**不并行**：备份整库脚本在 `strings.Builder`
  内存拼完一次写盘、导入 `io.ReadAll` 整读（注释自认"几百 MB 的 dump 同步读会冻结窗口且内存
  翻倍"）、行级 cap（备份 10 万、导出 2000）。单机内存模型的诚实边界；大文件流化（分块读、
  边读边写）是论文语境下唯一的修正方向。
- **NewSQL/分布式的缺席**：无分片、无并行查询、无 2PC——单连接=单服务器会话。这不是缺陷，
  是定位。

---

## 4. 论文镜鉴出的可操作建议（按性价比排序）

1. **补 admission control**（fntdb07 §2.4）：给内置 MySQL `sql.DB` 设 `SetMaxOpenConns`（如 4-8）
   并把 SessionPool 上限暴露到连接设置。当前过载只受服务器 `max_connections` 保护，客户端无背压。
2. **扫描器收敛**（tahboub18 单遍哲学）：把 IsRowReturning/只读判定/语句切分统一进内核
   `sqltext`（一个扫描器、四处消费），先消灭 backup.go 与 query.go 两套切分器的块注释行为分叉。
3. **编辑缓冲持久化**（ARIES 精神）：pending 变更追加写本地日志，窗口/进程崩溃后提示恢复；
   顺带解决"翻页丢编辑"——当前 `loadTablePage` 直接换网格，dirty 全弃（`workspace.go:2157-2192`）。
4. **多结果集**：`Run All` 至少给每条语句保留可切换的结果页签（database/sql 本身支持
   `NextResultSet`），替代现在"只有最后一条 + 手动 Pin"。
5. **同源生成补齐**（tahboub18）：INSERT 预览的 DEFAULT 列渲染改为与执行一致的"省略该列"，
   让预览 SQL 与执行 SQL 字节级同源。
6. **Redis 双模式**（红皮书 one-size-fits-all）：KV 会话给专用浏览/命令双面板（能力接口
   已有七七八八），逐步退出"key 当表"的伪装。
7. 小项：SSH HostKey 校验补 known_hosts（论文意义上的安全通信收尾）；`friendlyErr` 目前
   覆盖编辑/导入路径而查询错误仍走原始弹窗（`query.go:642, 840`），可统一。

## 5. 一句话总结

**fynequery = fntdb07 里"client tier + utilities"两层的完整产品化**：进程模型（插件进程池 +
goroutine worker + 语句级 KILL）实现得比多数同类更"论文"，缓冲池/锁/优化器诚实外包给服务器并
在客户端留下镜像（虚拟网格、乐观写回校验、目录缓存、分类扫描器）；它的 SQL 生成器单遍同源、
方言层"字符串进字符串出"，与 tahboub18 的简单性哲学不谋而合；它最大的偏离（Redis 表形伪装、
无 admission control、编辑缓冲不持久、四套扫描器并存）恰好都落在三篇文献画出的边界线上——
也是下一轮迭代的现成路线图。
