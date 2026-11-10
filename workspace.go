package main

// The workspace window: toolbar with connection breadcrumb, document tabs,
// sidebar tree, data grid / SQL editor, bottom segmented control + status
// bar. All data comes from the live db.Session handed to openWorkspace.

import (
	"errors"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/db"
	"fynequery/icons"
	"fynequery/proto"
)

// browseLimit is how many rows a table data tab loads (one page).
const browseLimit = 300

// docKind distinguishes the two document-tab flavours in the strip.
type docKind int

const (
	docData  docKind = iota // a browsed table/view
	docQuery                // an SQL editor pane
)

// dataTableTab is one open table's view state. The grid widget, paging cursor,
// filter and structure/index panes are stored per tab so several tables stay
// open at once and switching restores each one exactly as it was.
type dataTableTab struct {
	name        string
	grid        *dataTable
	cols        []ColumnInfo
	offset      int
	totalRows   int
	totalApprox bool
	filterSQL   string
	segIdx      int // which of Data/Structure/Indexes is showing
	structObj   fyne.CanvasObject
	indexObj    fyne.CanvasObject
	gen         int // per-tab load generation (discards stale async results)
	loaded      bool
	// kvNext is the Redis value pager's continuation cursor ("" = exhausted
	// or not a paged collection). The 加载更多值 button is driven by it.
	kvNext string
	// carryEdits hold unsaved edits from pages the user flipped away from
	// (建议3: paging used to silently discard them). PK-keyed; staged inserts
	// always ride here because a staged row exists on no server page.
	// carryPageEdits owns the lifecycle: capture on flip, re-apply onto a
	// page that shows the row, consume on commit, drop on rollback.
	carryUpdates []rowUpdate
	carryDeletes []map[string]*string
	carryInserts []map[string]*string
	// pageDone counts completed loadTablePage callbacks (atomic). A bump is
	// the happens-before edge after which the swapped grid and its applied
	// carry-over are safe to read from another goroutine.
	pageDone atomic.Uint64
	// kvMeta is the KV key's server-reported type and expiry (nil until the
	// async fetch lands). The details pane shows it while no element row is
	// selected — the 键视图's answer to a SQL table's column structure.
	kvMeta *db.KeyMeta
}

// kvMode reports whether the current connection is a KV store (Redis today):
// keys are not tables, so the data view drops the SQL disguises (structure /
// indexes segments, filter / column pickers) and the details pane shows key
// metadata. Same verdict the sidebar's kv inventory uses.
func (ws *workspace) kvMode() bool {
	return ws.conn.Driver == "redis"
}

// deleteActiveKey deletes the active tab's whole key (KV sessions). The
// confirm dialog and Safe Mode gate live in deleteKey.
func (ws *workspace) deleteActiveKey() {
	t := ws.activeData
	if t == nil || !ws.kvMode() {
		return
	}
	ka, ok := db.AsKeyAdmin(ws.sess)
	if !ok {
		return
	}
	ws.deleteKey(t.name, ka)
}

// kvDetailsFields renders a key's server metadata as the field list the
// details pane displays while no element row is selected. elements is the
// element count of the currently fetched window (the only count the UI
// truthfully knows — a SCAN/MATCH key list never counted the key's members).
// onTTL, when non-nil, makes the TTL card editable in place (it receives the
// rendered TTL text and shows the edit dialog); nil leaves it read-only.
func kvDetailsFields(key string, meta *db.KeyMeta, elements int, onRename, onTTL func(current string)) []detailsField {
	ttl := "永不过期"
	if meta != nil && meta.HasTTL {
		if meta.TTL > 0 {
			ttl = "剩余 " + meta.TTL.String()
		} else {
			ttl = "已过期"
		}
	}
	kind := "未知"
	if meta != nil && meta.Kind != "" {
		kind = meta.Kind
	}
	n := strconv.Itoa(elements)
	return []detailsField{
		// Tapping the key name renames it in place (renameKey refuses to
		// overwrite an existing key, so a typo cannot destroy another key).
		{Name: "键", Value: &key, Edit: onRename},
		{Name: "类型", Value: &kind, ReadOnly: true},
		{Name: "TTL", Value: &ttl, Edit: onTTL},
		{Name: "元素数(本窗口)", Value: &n, ReadOnly: true},
	}
}

// ttlParseSeconds parses a TTL input as seconds (0 = never expire / PERSIST).
// It also accepts Go duration spellings ("30s", "5m", "1h30m") because a
// bare "90" and a human "90s" should not mean different things.
func ttlParseSeconds(input string) (int64, error) {
	input = strings.TrimSpace(input)
	if secs, err := strconv.ParseInt(input, 10, 64); err == nil {
		if secs < 0 {
			return 0, errors.New("秒数不能为负数；0 表示永不过期")
		}
		return secs, nil
	}
	d, err := time.ParseDuration(input)
	if err != nil {
		return 0, errors.New("请输入整数秒数或时长（如 30s / 5m / 1h30m）；0 表示永不过期")
	}
	if d <= 0 {
		return 0, errors.New("时长必须为正；0 表示永不过期")
	}
	secs := int64(d / time.Second)
	if secs <= 0 {
		return 0, errors.New("过期时间过小（不足 1 秒）。")
	}
	return secs, nil
}

// editKeyTTL opens the TTL editor for a key and applies the new expiry
// through Safe Mode. Shared by the sidebar key menu and the details-pane
// in-place TTL edit.
func (ws *workspace) editKeyTTL(name, current string, ka db.KeyAdmin) {
	dialog.ShowEntryDialog("设置 TTL — "+name,
		"过期秒数或时长（如 30s / 5m / 1h30m）；0 = 永不过期\n当前："+current, func(input string) {
			secs, err := ttlParseSeconds(input)
			if err != nil {
				showInfo(ws.window, "设置 TTL", err.Error())
				return
			}
			ttl := time.Duration(secs) * time.Second
			if secs > 0 && ttl <= 0 {
				showInfo(ws.window, "设置 TTL", "过期时间过大。")
				return
			}
			desc := fmt.Sprintf("PERSIST %q", name)
			if secs > 0 {
				desc = fmt.Sprintf("PEXPIRE %q %d", name, ttl.Milliseconds())
			}
			gateSafeMode(ws.window, ws.conn, desc, func() {
				safeGo("setKeyTTL "+name, func() {
					err := ka.SetKeyTTL(name, ttl)
					safeDo(func() {
						if err != nil {
							showInfo(ws.window, "设置 TTL", err.Error())
							return
						}
						ws.logf("已设置键 %s 的 TTL：%s", name, desc)
						// The kind never changes on a TTL edit; only expiry does.
						if t := ws.activeData; t != nil && t.name == name {
							kind := ""
							if t.kvMeta != nil {
								kind = t.kvMeta.Kind
							}
							t.kvMeta = &db.KeyMeta{Kind: kind, HasTTL: secs > 0, TTL: ttl}
						}
						ws.refreshRowDetails()
					})
				})
			})
		}, ws.window)
}

// docTab ties one strip entry to its content (a table view or a query pane).
type docTab struct {
	kind docKind
	data *dataTableTab
	qp   *queryPane
}

type workspace struct {
	window     fyne.Window
	conn       Connection
	sess       db.Session
	dbType     DBType
	side       *sidebar
	tabs       *tabBar
	content    *fyne.Container // stacked tab contents
	status     *statusBar
	segment    *segmented
	structPane *fyne.Container
	indexPane  *fyne.Container   // 索引 segment content
	dataStack  *fyne.Container   // dataSlot + struct + index (tab 0 content)
	queryTabs  []*queryPane      // one independent query pane per SQL tab
	docs       []*docTab         // parallel to ws.tabs.tabs (data or query)
	dataTabs   []*dataTableTab   // open table views (multi-tab browsing)
	activeData *dataTableTab     // the table tab currently on screen
	journal    journalWriter     // serializes pending-edit journal disk writes
	details    *detailsPane      // right sidebar
	detailWrap fyne.CanvasObject // toggled by the toolbar button
	currentDB  string            // selected database ("" = none yet)
	// dbSwitchSeq guards the background database dial: a switch superseded by a
	// newer one drops its result instead of swapping the session underneath it.
	dbSwitchSeq      uint64
	dataSlot         *fyne.Container // holds the current data grid (replaced per table)
	grid             *dataTable
	tableName        string
	tableCols        []ColumnInfo // structure of the open table (for the 详情 pane)
	offset           int          // first row index of the current page
	totalRows        int          // last known total for the open table
	totalApprox      bool
	filterSQL        string          // active WHERE clause for the data view ("" = none)
	hiddenCols       map[string]bool // data-view columns hidden via the Columns dialog
	editMode         bool            // editable-grid mode (double-click to change cells)
	editBtn          *clickableIcon  // toolbar edit-mode toggle
	commitBtn        *flatButton
	rollbackBtn      *flatButton
	previewBtn       *flatButton
	dirtyLabel       *widget.Label
	txnRow           *fyne.Container // bottom-bar row holding the transaction strip
	prevBtn          *flatButton
	nextBtn          *flatButton
	moreValBtn       *flatButton       // Redis 值页「加载更多值」（kvNext 驱动显隐）
	bottomBar        fyne.CanvasObject // data-view bottom bar (hidden in query tabs)
	pendingDDL       []string          // queued ALTER TABLE statements (Preview/Commit)
	findCounterLabel *widget.Label     // "cur/total" counter for the data-grid find box
	filterBtn        *flatButton       // bottom-bar Filter button (shows an active-filter dot)
	columnsBtn       *flatButton       // bottom-bar Columns picker (hidden for KV keys)
	pageMarkBtn      *flatButton       // bottom-bar ≡ shortcut to the column picker
	deleteKeyBtn     *flatButton       // bottom-bar 删除键 (KV only: whole-key DEL)
	loadingMu        sync.Mutex
	loadingGen       int                // generation counter to discard stale async results
	searchTimer      *time.Timer        // pending KV key-filter debounce
	searchSeq        int                // drops a debounced filter overtaken by newer typing
	keepAliveStop    chan struct{}      // closed when the workspace closes, ending the keep-alive ticker
	invCache         []proto.ObjectInfo // last fetched catalog (approxRows lookup); guarded by loadingMu
	// toolbar
	ribbon        *serverRibbon     // connection ribbon (opens the connection picker)
	ribbonTLS     string            // TLS version shown in the ribbon ("" = omit)
	dbSelect      *clickableIcon    // database selector icon (opens 打开数据库)
	sideBtn       *clickableIcon    // toolbar sidebar toggle (selected state)
	detailBtn     *clickableIcon    // toolbar details toggle
	consoleBtn    *clickableIcon    // toolbar console toggle
	txGroup       fyne.CanvasObject // manual-transaction toolbar group (hidden if unsupported)
	txBegin       *clickableIcon    // Begin Transaction (selected while a transaction is open)
	txCommit      *clickableIcon    // Commit the open transaction
	txRollback    *clickableIcon    // Roll back the open transaction
	sideWrap      fyne.CanvasObject // left sidebar wrapper (toggled by the toolbar)
	body          fyne.CanvasObject // root container (refreshed after panel toggles)
	welcomeReturn func()            // brings the welcome window back after close
	skipWelcome   bool              // suppress welcomeReturn on the next close
	// bottom console log (mockup "控制台日志")
	console        *consolePane
	consoleWrap    fyne.CanvasObject
	consoleSplit   *container.Split
	consoleVisible bool
	// document tab strip; hidden while the data tab is alone
	tabStrip fyne.CanvasObject
}

// openWorkspace dials nothing: the caller passes an established session.
// onClose (optional) is invoked after the workspace window closes — used to
// bring the welcome page back, like TablePlus.
func openWorkspace(app fyne.App, conn Connection, sess db.Session, dark bool, onClose func()) {
	darkMode = dark
	w := app.NewWindow("TablePro — " + conn.Name)
	want := fyne.NewSize(1360, 860)
	w.Resize(want)

	// The server-level re-dial (TablePlus opens at the server with no
	// database selected) moved below the workspace build: it used to run
	// here on the UI thread with the window already on screen, and a slow
	// dial froze the half-built workspace for the whole round trip.
	ws := &workspace{
		window: w, conn: conn, sess: sess,
		dbType: dbTypeByDriver(conn.Driver), currentDB: conn.Database,
		welcomeReturn: onClose,
	}
	// Closing the workspace closes the database session. The flag stops the
	// intercepted Close from re-entering itself (Fyne re-invokes the
	// intercept on every Close until the flag guards it).
	closing := false
	w.SetCloseIntercept(func() {
		if closing {
			return
		}
		closing = true
		// End the keep-alive goroutine first so it never observes a torn
		// session swap, then close the session under the same lock.
		if ws.keepAliveStop != nil {
			close(ws.keepAliveStop)
			ws.keepAliveStop = nil
		}
		if old := ws.liveSession(); old != nil {
			old.Close()
			ws.setSession(nil)
		}
		w.Close()
		// Switching connections from the toolbar opens the next workspace
		// itself — don't also pop the welcome page up.
		if !ws.skipWelcome && onClose != nil {
			onClose() // return to the welcome page
		}
	})
	w.SetContent(newPanelBackground(ws.build(), func() color.Color { return pal().bg }))
	ws.updateBanner()
	ws.loadInventory()
	ws.loadDatabases()
	ws.loadTLS()
	ws.offerEditRestore()

	// Server-level re-dial, now async: land on the db-bound session (the
	// documented fallback for drivers that require a database) and swap in
	// the server-level one when the dial completes. Same dbSwitchSeq guard
	// as switchDatabase/reconnect — a user switch or reconnect supersedes
	// this dial. Redis is skipped: it has no server-level connection
	// distinct from DB 0.
	if conn.Database != "" && conn.Driver != "redis" {
		serverConn := conn
		serverConn.Database = ""
		ws.dbSwitchSeq++
		seq := ws.dbSwitchSeq
		safeGo("serverDial", func() {
			ss, err := dialConn(serverConn)
			safeDo(func() {
				// liveSession()==nil means the workspace window was closed
				// while the dial was in flight (CloseIntercept closed the
				// db-bound session) — the landed session must not attach.
				if seq != ws.dbSwitchSeq || ws.liveSession() == nil {
					// A switch/reconnect superseded this dial, or the user
					// closed the workspace while it was in flight — the
					// landed session would otherwise never be closed.
					if err == nil {
						ss.Close()
					}
					return
				}
				if err != nil {
					return // the db-bound fallback stands
				}
				if old := ws.liveSession(); old != nil {
					old.Close()
				}
				ws.setSession(ss)
				ws.conn = serverConn
				ws.applyKeyFilter(ss)
				ws.updateBanner()
				ws.loadInventory()
				ws.loadDatabases()
				ws.loadTLS()
			})
		})
	}
	// Ctrl+K opens the quick-open palette (TablePlus OpenAnything). The
	// database picker remains reachable from the toolbar database pill.
	w.Canvas().AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyK, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) { ws.showQuickOpen() })
	// Ctrl+F toggles the in-result search bar (hidden by default).
	w.Canvas().AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyF, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) {
			if qp := ws.activeQueryPane(); qp != nil {
				qp.toggleFindBar()
			}
		})
	w.Show()
	// Strip the native caption so the title row above becomes the window's top
	// row. Deferred past the first frames: glfw must have finished its initial
	// client-area sizing first. enableCustomChrome also resizes the native
	// window to the canvas — a Fyne Resize afterwards would re-add the removed
	// caption margins and leave a blank band with misaligned pointer hits.
	// The one-shot is armed through the window-timer group so a close before it
	// fires cancels it (FQ-10).
	armWindowTimer(w, 300*time.Millisecond, func() {
		safeDo(func() { enableCustomChrome(w) })
	})
	// Refresh the theme only now that the window has a native handle; pushing
	// the app theme earlier would make Fyne walk every window's HWND for
	// dark-mode and panic on any that is not yet shown.
	applyTheme(w)
	// The window was sized before the monitor scale was known; re-apply it so
	// the canvas matches the real client area (otherwise the content is
	// rendered larger than the window and clipped).
	fitWindowWhenScaled(w, want)
	ws.startKeepAlive()
}

// serverLabel renders "server : host" for the connection banner.
func (ws *workspace) serverLabel() string {
	host := ws.conn.Host
	if ws.sess != nil {
		if srv := ws.sess.Server(); srv != "" {
			if host != "" {
				return srv + " : " + host
			}
			return srv
		}
	}
	return host
}

// updateBanner refreshes the toolbar database selector pill (the current
// database, or a "选择数据库" hint when none is picked) and the green
// server-info ribbon.
// dbPickTip is the hover description for the toolbar database icon: the
// TablePlus-style hint plus the currently selected database, if any.
func dbPickTip(current string) string {
	if current == "" {
		return "显示数据库列表（Ctrl K）"
	}
	return current + " — 显示数据库列表（Ctrl K）"
}

func (ws *workspace) updateBanner() {
	if ws.dbSelect != nil {
		ws.dbSelect.setTip(dbPickTip(ws.currentDB))
	}
	ws.refreshRibbon()
}

// ribbonEnv labels the connection as local or remote (TablePlus shows "LOCAL"
// for loopback hosts).
func (ws *workspace) ribbonEnv() string {
	switch strings.ToLower(ws.conn.Host) {
	case "", "localhost", "127.0.0.1", "::1":
		return "LOCAL"
	}
	return "REMOTE"
}

// refreshRibbon recomputes the green server-info bar from the live session:
// "ENV | server : tls : host : db : table". The table segment is only shown
// on the data tab with a table open. The background follows the connection's
// status colour when one is set.
func (ws *workspace) refreshRibbon() {
	if ws.ribbon == nil {
		return
	}
	srv := ""
	if ws.sess != nil {
		srv = ws.sess.Server()
	}
	if srv == "" {
		srv = ws.dbType.Name
	}
	table := ""
	if doc := ws.docAt(ws.tabs.current); doc != nil && doc.kind == docData {
		table = ws.tableName
	}
	// Privacy mode redacts where the server lives on the ribbon.
	host, db := ws.conn.Host, ws.currentDB
	if privacyOn() {
		host = maskedPart(host)
		db = maskedPart(db)
	}
	ws.ribbon.update(ws.ribbonEnv(), srv, ws.ribbonTLS, host, db, table)
	if h := ws.conn.StatusColorHex(); h != "" {
		ws.ribbon.SetStatusColor(hex(h))
	} else {
		ws.ribbon.SetStatusColor(nil)
	}
}

// openDatabasePicker shows the "打开数据库" sheet for the current server.
func (ws *workspace) openDatabasePicker(pos fyne.Position) {
	if ws.sess == nil {
		return
	}
	showOpenDatabaseDialog(ws.window, pos, ws.sess, ws.conn.Driver, ws.currentDB, func(name string) {
		ws.switchDatabase(name)
	})
}

// openConnectionPicker lists the saved connections (TablePlus: click the
// connection breadcrumb → pick another connection). Picking one dials it and
// opens a fresh workspace window for it.
func (ws *workspace) openConnectionPicker() {
	conns, err := loadConnections()
	if err != nil {
		showInfo(ws.window, "选择连接", "Could not read saved connections:\n"+err.Error())
		return
	}
	if len(conns) == 0 {
		showInfo(ws.window, "选择连接", "没有已保存的连接。\n请在欢迎页创建连接。")
		return
	}
	showConnectionPicker(ws.window, conns, func(c Connection, sess db.Session) {
		ws.skipWelcome = true
		openWorkspace(fyne.CurrentApp(), c, sess, darkMode, ws.welcomeReturn)
		ws.window.Close()
	})
}

// openMoreMenu shows the toolbar "更多功能" popup.
func (ws *workspace) openMoreMenu() {
	privacyLabel := "隐私模式 开"
	if !privacyOn() {
		privacyLabel = "隐私模式 关"
	}
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("新建查询", func() { ws.addQueryTab() }),
		fyne.NewMenuItem("刷新", func() { ws.reloadTable() }),
		fyne.NewMenuItem("全屏", func() { ws.window.SetFullScreen(!ws.window.FullScreen()) }),
		fyne.NewMenuItem("深色/浅色", func() {
			darkMode = !darkMode
			applyTheme(ws.window)
		}),
		fyne.NewMenuItem(fmt.Sprintf("保活 ping(30s): %s", map[bool]string{true: "开", false: "关"}[keepAliveEnabled.Load()]), func() {
			keepAliveEnabled.Store(!keepAliveEnabled.Load())
			showInfo(ws.window, "连接保活",
				fmt.Sprintf("每 30 秒向服务器发一次 ping，防止空闲连接被防火墙/SSH 断开。当前：%s。",
					map[bool]string{true: "开", false: "关"}[keepAliveEnabled.Load()]))
		}),
		fyne.NewMenuItem(privacyLabel, func() { ws.togglePrivacy() }),
		fyne.NewMenuItem("ER Diagram", func() { ws.openERDiagram() }),
		fyne.NewMenuItem("Server Metrics", func() { ws.openServerMetrics() }),
		fyne.NewMenuItem("Import Data…", func() { ws.importData() }),
		fyne.NewMenuItem("User Management", func() { ws.openUserManagement() }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("侧边栏 显示/隐藏", func() { ws.toggleSidebar() }),
		fyne.NewMenuItem("详情面板 显示/隐藏", func() { ws.toggleDetails() }),
		fyne.NewMenuItem("控制台 显示/隐藏", func() { ws.toggleConsole() }),
	)
	pop := widget.NewPopUpMenu(menu, ws.window.Canvas())
	pop.ShowAtPosition(fyne.NewPos(88, 46))
}

// toggleSidebar shows/hides the left sidebar (项目/查询/历史记录 window).
func (ws *workspace) toggleSidebar() {
	if ws.sideWrap == nil || ws.body == nil {
		return
	}
	if ws.sideWrap.Visible() {
		ws.sideWrap.Hide()
	} else {
		ws.sideWrap.Show()
	}
	if ws.sideBtn != nil {
		ws.sideBtn.setSelected(ws.sideWrap.Visible())
	}
	ws.body.Refresh()
}

// toggleDetails shows/hides the right 详情/助理 pane.
func (ws *workspace) toggleDetails() {
	if ws.detailWrap == nil {
		return
	}
	if ws.detailWrap.Visible() {
		ws.detailWrap.Hide()
	} else {
		ws.detailWrap.Show()
	}
	if ws.detailBtn != nil {
		ws.detailBtn.setSelected(ws.detailWrap.Visible())
	}
	if ws.body != nil {
		ws.body.Refresh()
	}
}

// toggleConsole shows/hides the bottom log console. The split offset is
// pushed to 1 while hidden so the content reclaims the space.
func (ws *workspace) toggleConsole() {
	if ws.consoleWrap == nil {
		return
	}
	ws.consoleVisible = !ws.consoleVisible
	if ws.consoleVisible {
		ws.consoleWrap.Show()
		if ws.consoleSplit != nil {
			ws.consoleSplit.SetOffset(0.78)
		}
	} else {
		ws.consoleWrap.Hide()
		if ws.consoleSplit != nil {
			ws.consoleSplit.SetOffset(1)
		}
	}
	if ws.consoleBtn != nil {
		ws.consoleBtn.setSelected(ws.consoleVisible)
	}
	if ws.body != nil {
		ws.body.Refresh()
	}
}

// togglePrivacy flips privacy mode (host/db redaction) and reports the state.
func (ws *workspace) togglePrivacy() {
	on := togglePrivacyMode()
	ws.refreshRibbon()
	showInfo(ws.window, "隐私模式",
		fmt.Sprintf("隐私模式已%s。\n主机/端口/库名在界面中显示为 •••。", map[bool]string{true: "开启", false: "关闭"}[on]))
}

// logf appends one line to the bottom console. Call from the UI thread
// (event handlers or safeDo) only.
func (ws *workspace) logf(format string, args ...interface{}) {
	if ws.console != nil {
		ws.console.log(fmt.Sprintf(format, args...))
	}
}

// connInfoText renders the info-dialog text for the active connection
// (redacted in privacy mode).
func (ws *workspace) connInfoText() string {
	dbName := ws.currentDB
	if dbName == "" {
		dbName = "(未选择)"
	}
	return fmt.Sprintf("名称: %s\n驱动: %s\n服务器: %s\n主机: %s : %s\n用户: %s\n数据库: %s",
		ws.conn.Name, ws.conn.Driver, ws.serverLabel(),
		maskedPart(ws.conn.Host), maskedPart(ws.conn.Port), ws.conn.User, maskedPart(dbName))
}

// switchDatabase re-dials the connection bound to the chosen database and
// reloads the sidebar tree + banner.
func (ws *workspace) switchDatabase(name string) {
	if name == "" || name == ws.currentDB {
		return
	}
	newConn := ws.conn
	newConn.Database = name
	ws.dbSwitchSeq++
	seq := ws.dbSwitchSeq
	// 拨号可能很慢（Redis 10s 超时、插件冷启动 10s+5s）：同步做会冻结整个
	// 窗口。与 connpicker 同一范式——后台拨号 + 进度框，结果回 UI 线程。
	progress := dialog.NewProgressInfinite("打开数据库", "正在连接数据库 "+name+"…", ws.window)
	progress.Show()
	safeGo("switchDatabase", func() {
		// Same defer as reconnect: a panicking dialConn must still retire the
		// progress dialog.
		defer safeDo(func() { progress.Hide() })
		sess, err := dialConn(newConn)
		safeDo(func() {
			if seq != ws.dbSwitchSeq {
				// A newer switch superseded this dial: drop its session rather
				// than swapping it in under the user's newer choice.
				if err == nil {
					sess.Close()
				}
				return
			}
			if err != nil {
				showInfo(ws.window, "打开数据库", "无法连接到数据库 "+name+":\n"+err.Error())
				return
			}
			if ws.liveSession() != nil {
				ws.liveSession().Close()
			}
			ws.setSession(sess)
			ws.conn = newConn
			ws.currentDB = name
			ws.closeAllDataTabs() // open table grids belong to the old database
			ws.updateBanner()
			ws.logf("已选择数据库 %s", name)
			ws.syncChrome() // blank center until a table is opened
			if ws.conn.Driver == "redis" {
				// The new session starts with an empty pattern; silently clearing the
				// box avoids a search text that no longer filters anything.
				ws.side.resetSearch()
			}
			ws.loadInventory()
			ws.loadDatabases()
			ws.loadTLS()
		})
	})
}

// sideWidth is the initial width of the sidebar column (~18% of the window).
const sideWidth float32 = 250

// detailWidth is the initial width of the right 详情/助理 pane. Both panels are
// resizable afterwards by dragging their divider.
const detailWidth float32 = 230

// keep-alive + reconnect helpers (TablePlus "keep connection alive").

// keepAliveInterval is how often the live session is pinged so idle sockets
// (closed by servers/firewalls/SSH) stay open.
const keepAliveInterval = 30 * time.Second

// keepAliveEnabled is toggled from the 更多功能 menu; atomic because the
// keep-alive goroutine reads it on its own thread.
var keepAliveEnabled atomic.Bool

func init() { keepAliveEnabled.Store(true) }

// liveSession / setSession guard ws.sess against the keep-alive goroutine.
func (ws *workspace) liveSession() db.Session {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	return ws.sess
}

func (ws *workspace) setSession(s db.Session) {
	ws.loadingMu.Lock()
	ws.sess = s
	ws.invCache = nil // the previous session's catalog says nothing about the new one
	ws.loadingMu.Unlock()
	safeDo(ws.syncTxControls)
}

// startKeepAlive pings the session every 30s in the background. It reads the
// current session pointer each tick (so a database switch just follows), and
// is a no-op while keepAliveEnabled is off. SQLite connections (file-based,
// never idle-closed) skip the ping. The goroutine exits when the workspace's
// CloseIntercept closes keepAliveStop — without it every workspace window
// leaked one live goroutine (holding the whole workspace) after closing.
func (ws *workspace) startKeepAlive() {
	if ws.conn.Driver == "sqlite" {
		return
	}
	stop := make(chan struct{})
	ws.keepAliveStop = stop
	go func() {
		tick := time.NewTicker(keepAliveInterval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if !keepAliveEnabled.Load() {
				continue
			}
			sess := ws.liveSession()
			if sess == nil {
				continue
			}
			// A successful ping keeps the socket open; errors are swallowed
			// (the next tick retries). KV sessions answer with their own PING:
			// "SELECT 1" would mean something else entirely in Redis.
			safeGo("keepalive", func() {
				if p, ok := db.AsPinger(sess); ok {
					if err := p.Ping(); err != nil {
						// logf 触碰 Fyne widget，只能从 UI 线程调——此处是
						// 后台 goroutine，须经 safeDo。
						safeDo(func() { ws.logf("keepalive: %v", err) })
					}
					return
				}
				if _, err := sess.Query("SELECT 1", 1); err != nil {
					safeDo(func() { ws.logf("keepalive: %v", err) })
				}
			})
		}
	}()
}

// reconnect dials a fresh session for the workspace's connection and swaps it
// in (the timeout dialog's "Reconnect & Retry" action). It reports nil and
// shows an error when the re-dial fails.
// reconnect dials a fresh session in the background and hands it to done on
// the UI thread (nil on failure). The old synchronous return froze the window
// for the whole dial — up to 10–15s on a slow network or a cold plugin.
func (ws *workspace) reconnect(done func(db.Session)) {
	newConn := ws.conn
	newConn.Database = ws.currentDB
	progress := dialog.NewProgressInfinite("重连", "正在重新连接…", ws.window)
	progress.Show()
	// The dial may take 10s+; a database switch started meanwhile supersedes
	// it (same guard as switchDatabase). Without this check the stale dial's
	// completion would close the NEW database's session and roll ws.conn
	// back — silently undoing the user's switch and sending later queries
	// to the old database.
	ws.dbSwitchSeq++
	seq := ws.dbSwitchSeq
	safeGo("reconnect", func() {
		// The progress dialog is hidden on the goroutine's defer: safeDo below
		// never runs when dialConn panics (safeGo recovers), and an infinite
		// progress left behind freezes the reconnect affordance forever.
		defer safeDo(func() { progress.Hide() })
		sess, err := dialConn(newConn)
		safeDo(func() {
			if seq != ws.dbSwitchSeq {
				// A newer switch superseded this reconnect: drop the stale
				// session instead of swapping it in under the user's choice.
				if err == nil {
					sess.Close()
				}
				done(nil)
				return
			}
			if err != nil {
				showInfo(ws.window, "重连", "Reconnect failed:\n"+err.Error())
				done(nil)
				return
			}
			if old := ws.liveSession(); old != nil {
				old.Close()
			}
			ws.setSession(sess)
			ws.conn = newConn
			ws.applyKeyFilter(sess)
			ws.loadTLS()
			done(sess)
		})
	})
}

// ---- manual transactions (TablePlus "Begin Transaction" mode) --------------

// txSession returns the current session if it supports manual transactions.
func (ws *workspace) txSession() (db.Transactional, bool) {
	sess := ws.liveSession()
	if sess == nil {
		return nil, false
	}
	return db.AsTransactional(sess)
}

// syncTxControls shows the transaction toolbar group only when the session
// supports transactions, and reflects the open/closed state in the buttons:
// Begin is selected while a transaction is open; Commit/Rollback are disabled
// otherwise.
func (ws *workspace) syncTxControls() {
	if ws.txGroup == nil {
		return
	}
	tx, ok := ws.txSession()
	if !ok {
		ws.txGroup.Hide()
		return
	}
	ws.txGroup.Show()
	inTx := tx.InTransaction()
	ws.txBegin.setSelected(inTx)
	ws.txBegin.setDisabled(inTx)     // already in a transaction
	ws.txCommit.setDisabled(!inTx)   // only meaningful inside a transaction
	ws.txRollback.setDisabled(!inTx) // only meaningful inside a transaction
	ws.updateBanner()
}

// beginTransaction opens a transaction on the session and refreshes controls.
func (ws *workspace) beginTransaction() {
	tx, ok := ws.txSession()
	if !ok {
		showInfo(ws.window, "Transaction", "This connection does not support transactions.")
		return
	}
	if tx.InTransaction() {
		return
	}
	safeGo("tx.begin", func() {
		if err := tx.Begin(); err != nil {
			safeDo(func() { dialog.ShowError(err, ws.window) })
			return
		}
		safeDo(func() {
			ws.syncTxControls()
			ws.logf("BEGIN — transaction started")
		})
	})
}

// commitTransaction commits the open transaction and refreshes controls.
func (ws *workspace) commitTransaction() {
	tx, ok := ws.txSession()
	if !ok || !tx.InTransaction() {
		return
	}
	safeGo("tx.commit", func() {
		if err := tx.Commit(); err != nil {
			safeDo(func() { dialog.ShowError(err, ws.window) })
			return
		}
		safeDo(func() {
			ws.syncTxControls()
			ws.logf("COMMIT — transaction committed")
		})
	})
}

// rollbackTransaction rolls back the open transaction and refreshes controls.
func (ws *workspace) rollbackTransaction() {
	tx, ok := ws.txSession()
	if !ok || !tx.InTransaction() {
		return
	}
	safeGo("tx.rollback", func() {
		if err := tx.Rollback(); err != nil {
			safeDo(func() { dialog.ShowError(err, ws.window) })
			return
		}
		safeDo(func() {
			ws.syncTxControls()
			ws.logf("ROLLBACK — transaction rolled back")
		})
	})
}

func (ws *workspace) build() fyne.CanvasObject {
	ws.tableName = ""

	// ---- tab bar (lives inside the top strip) ----
	ws.tabs = newTabBar(func(i int) {
		ws.tabs.setActive(i)
		ws.showTab(i)
	}, func() {
		ws.addQueryTab()
	})
	ws.tabs.onCloseTab = ws.closeTab
	// No seed tab: the centre stays blank until a table or query is opened,
	// and the strip only appears once a second document exists (mockup).
	ws.tabs.current = 0
	// The document tab strip sits at the top of the centre column so it lines
	// up on one row with the sidebar (项目/查询/历史) and details (详情/帮助)
	// mode switchers instead of occupying its own full-width band.
	ws.tabStrip = newPanelBackground(container.NewPadded(ws.tabs),
		func() color.Color { return pal().stripBG })
	ws.tabStrip.Hide()

	// ---- sidebar (resizable, like TablePlus) ----
	ws.side = newSidebar(func(name string) {
		ws.openObjectByName(name)
	}, func() { ws.addQueryTab() })
	ws.side.onDBPick = func(name string) { ws.switchDatabase(name) }
	ws.side.onNewDB = func() { ws.newDatabase() }
	ws.side.onItemMenu = func(name string, pos fyne.Position) { ws.showItemMenu(name, pos) }
	ws.side.onMoreKeys = ws.loadMoreKeys
	ws.side.onHistoryPick = func(stmt string) { ws.loadHistoryIntoEditor(stmt) }
	ws.side.onSearch = ws.onSidebarSearch
	ws.side.onQueryRun = func(q SavedQuery) { ws.openSavedQuery(q, true) }
	ws.side.onQueryOpen = func(q SavedQuery) { ws.openSavedQuery(q, false) }
	ws.side.onQuerySave = func() { ws.saveCurrentQuery() }
	ws.side.onQueryMenu = func(q SavedQuery, pos fyne.Position) { ws.showQueryMenu(q, pos) }
	ws.side.kv = ws.conn.Driver == "redis"
	ws.sideWrap = newPanelBackground(ws.side, func() color.Color { return pal().sidebarBG })

	// ---- status bar + grid (empty until a table is opened) ----
	data := emptyTableData()
	ws.status = newStatusBar()
	ws.grid = newDataTable(data, ws.status)
	ws.bindDataFind()
	ws.dataSlot = container.NewStack(ws.grid)

	// ---- right details sidebar (详情/助理, resizable) ----
	ws.details = newDetailsPane()
	ws.details.onEdit = ws.editRowField
	ws.details.onSet = ws.setRowField
	ws.details.onView = func(f detailsField) {
		val := ""
		if f.Value != nil {
			val = *f.Value
		}
		showFullValueDialog(ws.window, f.Name, val)
	}
	ws.detailWrap = newPanelBackground(ws.details, func() color.Color { return pal().detailBG })

	// ---- structure + index panes (segments) ----
	ws.structPane = container.NewStack(newStructurePane(emptyTableData()))
	ws.structPane.Hide()
	ws.indexPane = container.NewStack(newListPane(
		[]string{"Name", "Column", "Unique", "Type"}, nil))
	ws.indexPane.Hide()
	ws.dataStack = container.NewStack(ws.dataSlot, ws.structPane, ws.indexPane)
	// SQL panes are created on demand (one per tab), so the content stack
	// starts with just the data stack and grows as query tabs open. TablePlus
	// keeps each document tab's view in its own controller.
	ws.content = container.NewStack(ws.dataStack)

	// bottom: single TablePlus-style bar (segment + range text + paging).
	// Shown only on the data tab; the query tab has its own status bar and,
	// like TablePlus's query workspace, no 数据/结构/索引 segmented control.
	ws.bottomBar = container.NewVBox(ws.buildBottomBar())
	centerCol := container.NewBorder(ws.tabStrip, ws.bottomBar, nil, nil, ws.content)

	// Bottom console log (mockup "控制台日志"): part of the centre column so
	// the sidebars keep running the full window height.
	ws.console = newConsolePane()
	ws.consoleWrap = newPanelBackground(ws.console,
		func() color.Color { return pal().contentBG })
	ws.consoleVisible = true
	ws.consoleSplit = container.NewVSplit(centerCol, ws.consoleWrap)
	ws.consoleSplit.SetOffset(0.78)
	right := ws.consoleSplit

	// Resizable columns: sidebar | content | details. The split divider is the
	// accent-coloured border and can be dragged to resize each panel.
	avail := float32(1280)
	if c := ws.window.Canvas(); c != nil && c.Size().Width > 0 {
		avail = c.Size().Width
	}
	rightSplit := newAccentSplit(right, ws.detailWrap)
	rightSplit.SetOffset(float64((avail - detailWidth) / avail))
	center := newAccentSplit(ws.sideWrap, rightSplit)
	center.SetOffset(float64(sideWidth / avail))

	// ---- top strip: toolbar + document tabs ----
	strip := ws.buildStrip()

	body := container.NewBorder(strip, nil, nil, nil, center)
	ws.body = body
	ws.syncChrome() // start blank until a table is opened
	return body
}

// fetchFunctions lists stored functions/routines for the 函数 tree group.
// Best-effort: unsupported drivers simply yield nothing. It issues a real
// query, so it must run on a background goroutine; driver and database are
// snapshots so the worker never reads mutable workspace state.
func (ws *workspace) fetchFunctions(sess db.Session, driver, curDB string) []TableInfo {
	if sess == nil || curDB == "" {
		return nil
	}
	var q string
	switch driver {
	case "redis":
		return nil // keyspace, not routines
	case "postgres", "postgresql":
		q = "SELECT routine_name FROM information_schema.routines " +
			"WHERE routine_schema = current_schema() AND routine_type = 'FUNCTION' ORDER BY routine_name"
	case "mssql":
		q = "SELECT name FROM sys.objects WHERE type IN ('FN','IF','TF') ORDER BY name"
	case "oracle":
		// Oracle 没有 information_schema；此前落进 default 的 MySQL 配方必然
		// 失败且被静默吞掉——Oracle 的侧栏从此没有函数列表。
		q = "SELECT object_name FROM all_objects WHERE object_type = 'FUNCTION' AND owner = USER ORDER BY object_name"
	default: // mysql / mariadb
		q = "SELECT routine_name FROM information_schema.routines " +
			"WHERE routine_schema = DATABASE() AND routine_type = 'FUNCTION' ORDER BY routine_name"
	}
	res, err := sess.Query(q, 5000)
	if err != nil {
		ws.logf("函数列表：%v", err)
		return nil
	}
	var out []TableInfo
	for _, row := range res.StringRows() {
		if len(row) > 0 && row[0] != nil {
			out = append(out, TableInfo{Name: *row[0], Kind: "function"})
		}
	}
	return out
}

// loadInventory fetches tables/views (and the server string) in the
// background and feeds the sidebar. With no database selected it just shows
// the "未选择数据库" prompt.
func (ws *workspace) loadInventory() {
	if ws.currentDB == "" {
		ws.side.setNoDatabase()
		return
	}
	gen := ws.nextGen()
	sess := ws.liveSession()
	if sess == nil {
		return
	}
	// Snapshots: the worker must not read ws.sess / ws.conn, which the
	// reconnect/close paths swap concurrently.
	driver, curDB := ws.conn.Driver, ws.currentDB
	safeGo("loadInventory", func() {
		objs, err := sess.Tables()
		// fetchFunctions is its own query — it belongs in this background
		// goroutine; calling it inside the safeDo block below froze the UI
		// for the length of the round-trip.
		fns := ws.fetchFunctions(sess, driver, curDB)
		safeDo(func() {
			if !ws.current(gen) || ws.liveSession() != sess {
				return
			}
			if err != nil {
				ws.side.setLoadError(err.Error()) // inline, persistent (was a transient dialog)
				return
			}
			list := make([]TableInfo, 0, len(objs)+len(fns))
			for _, o := range objs {
				list = append(list, TableInfo{Name: o.Name, Kind: o.Kind, Rows: o.Rows, Approx: o.ApproxRows})
			}
			list = append(list, fns...)
			ws.side.setInventory(list, ws.currentDB)
			ws.loadingMu.Lock()
			ws.invCache = objs // approxRows reads the catalog from here, no re-pull
			ws.loadingMu.Unlock()
			if kp, ok := db.AsKeyPager(sess); ok {
				ws.side.setMoreKeys(kp.MoreKeys())
			} else {
				ws.side.setMoreKeys(false)
			}
			if srv := sess.Server(); srv != "" {
				ws.window.SetTitle("TablePro — " + ws.conn.Name + " — " + srv)
			}
			ws.refreshRibbon()
			// Like TablePlus: selecting a database only loads the tree — the
			// center stays blank until the user opens a table.
			ws.syncChrome()
		})
	})
}

// sidebarSearchDelay debounces KV key filtering: every keystroke would
// otherwise trigger a server-side scan over the whole keyspace.
const sidebarSearchDelay = 250 * time.Millisecond

// onSidebarSearch forwards the sidebar query to sessions that filter their
// inventory on the server (KV stores). It is a no-op for SQL sessions, whose
// search stays a local filter over the loaded table list.
func (ws *workspace) onSidebarSearch(text string) {
	sess := ws.liveSession()
	kf, ok := sess.(db.KeyFilter)
	if !ok {
		return
	}
	ws.loadingMu.Lock()
	ws.searchSeq++
	seq := ws.searchSeq
	if ws.searchTimer != nil {
		ws.searchTimer.Stop()
	}
	ws.searchTimer = time.AfterFunc(sidebarSearchDelay, func() {
		safeDo(func() {
			ws.loadingMu.Lock()
			stale := seq != ws.searchSeq
			ws.loadingMu.Unlock()
			// A replaced session (database switch, reconnect) owns a fresh,
			// unfiltered keyspace; its filter is set at swap time instead.
			if stale || ws.liveSession() != sess {
				return
			}
			kf.SetKeyFilter(text)
			ws.loadInventory()
		})
	})
	ws.loadingMu.Unlock()
}

// applyKeyFilter re-applies the sidebar search to a freshly dialled KV session
// so a reconnect keeps the key inventory the user was looking at.
func (ws *workspace) applyKeyFilter(sess db.Session) {
	kf, ok := sess.(db.KeyFilter)
	if !ok || ws.side == nil || ws.side.search == nil {
		return
	}
	kf.SetKeyFilter(ws.side.search.Text)
}

// loadMoreKeys appends the next key page (KV only) and feeds the sidebar. The
// session keeps the SCAN cursor, so repeated calls walk the whole keyspace
// without rescanning the keys already shown.
func (ws *workspace) loadMoreKeys() {
	sess := ws.liveSession()
	kp, ok := db.AsKeyPager(sess)
	if !ok || !kp.MoreKeys() {
		return
	}
	safeGo("loadMoreKeys", func() {
		_, _, err := kp.LoadMoreKeys()
		safeDo(func() {
			if ws.liveSession() != sess {
				return
			}
			if err != nil {
				showInfo(ws.window, "加载更多键", err.Error())
				return
			}
			// The page is now in the session cache, so this re-render is cheap
			// (no extra scan) and also refreshes the "load more" row.
			ws.loadInventory()
		})
	})
}

// loadDatabases fills the sidebar's bottom selector with the server's
// databases in the background. Keys off the session pointer so it does not
// disturb the inventory generation guard.
func (ws *workspace) loadDatabases() {
	sess := ws.sess
	if sess == nil {
		return
	}
	safeGo("loadDatabases", func() {
		dbs := listDatabases(sess, ws.conn.Driver)
		if len(dbs) == 0 {
			return
		}
		safeDo(func() {
			if ws.sess != sess {
				return // session was replaced while the query ran
			}
			ws.side.setDatabases(dbs, ws.currentDB)
		})
	})
}

// newDatabase is the sidebar "+" handler: opens the in-app "新建数据库" sheet.
// On success it switches the workspace to the newly created database.
func (ws *workspace) newDatabase() {
	if ws.sess == nil {
		return
	}
	showCreateDatabaseDialog(ws.window, ws.sess, ws.conn.Driver, func(name string) {
		ws.switchDatabase(name)
	})
}

// loadTLS fills the ribbon's TLS segment in the background (best effort).
// It keys off the session pointer rather than the load generation so it does
// not disturb the inventory guard.
func (ws *workspace) loadTLS() {
	sess := ws.sess
	if sess == nil {
		return
	}
	safeGo("loadTLS", func() {
		v := ws.fetchTLS(sess)
		if v == "" {
			return
		}
		safeDo(func() {
			if ws.sess != sess {
				return // session was replaced while the query ran
			}
			ws.ribbonTLS = v
			ws.refreshRibbon()
		})
	})
}

// fetchTLS returns the connection's negotiated TLS version ("TLSv1.2", …) or
// "" when the driver/connection exposes none (unknown driver, plaintext
// session, or a query error). Dialect queries are best effort; failures are
// silently ignored so the segment is simply omitted.
func (ws *workspace) fetchTLS(sess db.Session) string {
	var q string
	val := func(row []*string) *string {
		if len(row) > 0 {
			return row[0]
		}
		return nil
	}
	switch ws.conn.Driver {
	case "postgres", "postgresql":
		q = "SELECT version FROM pg_stat_ssl WHERE pid = pg_backend_pid()"
	case "mysql", "mariadb":
		// SHOW … returns (Variable_name, Value).
		q = "SHOW SESSION STATUS LIKE 'Ssl_version'"
		val = func(row []*string) *string {
			if len(row) > 1 {
				return row[1]
			}
			return nil
		}
	default:
		return "" // mssql / oracle: no portable version query
	}
	res, err := sess.Query(q, 1)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	p := val(res.StringRows()[0])
	if p == nil {
		return ""
	}
	v := strings.TrimSpace(*p)
	if v == "" || strings.EqualFold(v, "ON") || strings.EqualFold(v, "OFF") {
		return ""
	}
	return v
}

// nextGen/start generation guard: async results from a replaced session are
// dropped.
func (ws *workspace) nextGen() int {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	ws.loadingGen++
	return ws.loadingGen
}

func (ws *workspace) current(gen int) bool {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	return ws.loadingGen == gen
}

// buildStrip is the redesigned workspace header: a single light-gray toolbar
// with white rounded chip groups, the wide connection ribbon in the centre and
// a document tab strip that only appears when more than the data tab is open.
func (ws *workspace) buildStrip() fyne.CanvasObject {
	// ---- connection ribbon (tap → pick a connection, dot → connection info) ----
	ws.ribbon = newServerRibbon()
	ws.ribbon.onTap = func() { ws.openConnectionPicker() }
	ws.ribbon.onInfo = func() {
		showInfo(ws.window, "Connection", ws.connInfoText())
	}
	ws.refreshRibbon()

	// ---- left: menu / more, connection switcher, view actions, db + SQL ----
	ws.sideBtn = newToolButton(theme.MenuIcon(), func() { ws.toggleSidebar() })
	ws.sideBtn.setSelected(true)
	menuGroup := newToolGroup(
		ws.sideBtn,
		newToolButton(theme.MoreVerticalIcon(), func() { ws.openMoreMenu() }),
	)

	connBtn := newRoundToolButton(theme.ComputerIcon(), func() { ws.openConnectionPicker() })

	viewGroup := newToolGroup(
		newToolButton(theme.ViewRefreshIcon(), func() { ws.reloadTable() }),
		newToolButton(theme.VisibilityIcon(), func() { ws.togglePrivacy() }),
		newToolButton(theme.GridIcon(), func() { ws.openERDiagram() }),
	)

	// Database selector is an icon-only button (TablePlus style); the current
	// database name lives in its hover tooltip, not in the toolbar text.
	ws.dbSelect = newToolButton(theme.StorageIcon(), func() {
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(ws.dbSelect)
		ws.openDatabasePicker(pos)
	})
	ws.dbSelect.setTip(dbPickTip(ws.currentDB))
	dbSQLGroup := newToolGroup(
		ws.dbSelect,
		newIconTextButton(theme.DocumentCreateIcon(), "SQL", func() { ws.addQueryTab() }),
	)

	left := container.NewHBox(menuGroup, spacerW(4), connBtn, spacerW(4),
		viewGroup, spacerW(4), dbSQLGroup)

	// ---- right: connection/refresh/settings, console/details toggles ----
	ws.editBtn = newToolButton(icons.Edit, func() { ws.toggleEditMode() })
	opsGroup := newToolGroup(
		newToolButton(theme.InfoIcon(), func() {
			showInfo(ws.window, "Connection", ws.connInfoText())
		}),
		ws.editBtn,
		newToolButton(theme.SettingsIcon(), func() { ws.openMoreMenu() }),
	)
	// Manual transaction controls (TablePlus "Begin Transaction" mode). Shown
	// only when the session supports transactions; Commit/Rollback enable only
	// while a transaction is open (see syncTxControls).
	ws.txBegin = newToolButton(theme.MediaPlayIcon(), func() { ws.beginTransaction() })
	ws.txBegin.setTip("Begin Transaction")
	ws.txCommit = newToolButton(theme.ConfirmIcon(), func() { ws.commitTransaction() })
	ws.txCommit.setTip("Commit Transaction")
	ws.txRollback = newToolButton(icons.Cancel, func() { ws.rollbackTransaction() })
	ws.txRollback.setTip("Rollback Transaction")
	ws.txGroup = newToolGroup(ws.txBegin, ws.txCommit, ws.txRollback)
	ws.consoleBtn = newToolButton(icons.Console, func() { ws.toggleConsole() })
	ws.consoleBtn.setSelected(true)
	ws.detailBtn = newToolButton(theme.ViewRestoreIcon(), func() { ws.toggleDetails() })
	ws.detailBtn.setSelected(true)
	paneGroup := newToolGroup(ws.consoleBtn, ws.detailBtn)
	right := container.NewHBox(opsGroup, spacerW(4), ws.txGroup, spacerW(4), paneGroup)
	if chromeEnabled {
		// custom chrome: the window buttons live in the title row
		winGroup := newToolGroup(
			newToolButton(icons.WinMin, chromeMinimize),
			newToolButton(icons.WinMax, chromeToggleMax),
			newToolButton(icons.Cancel, func() { ws.window.Close() }),
		)
		right = container.NewHBox(opsGroup, spacerW(4), ws.txGroup, spacerW(4), paneGroup, spacerW(6), winGroup)
	}
	ws.syncTxControls()

	// Keep only horizontal breathing room around the ribbon; the old padded
	// wrapper also added 8px of vertical padding and made the strip too tall.
	ribbonArea := container.NewBorder(nil, nil, spacerW(8), spacerW(8), ws.ribbon)
	toolRow := container.NewBorder(nil, nil, left, right, ribbonArea)
	// Empty title-row space stays draggable so the window can still be moved
	// (and double-click maximised) now that the native caption is gone.
	stack := container.NewStack(newChromeDragArea(), toolRow)
	toolBox := newPanelBackground(container.NewPadded(stack),
		func() color.Color { return pal().toolbarBG })

	// The document tab strip lives at the top of the centre column (see
	// build()), on the same row as the sidebar / details mode switchers.
	return toolBox
}

// chromeDragArea fills the title row behind the widgets: dragging empty space
// moves the window via the OS caption dragger, double-click toggles maximise.
type chromeDragArea struct {
	widget.BaseWidget
	lastDrag time.Time
}

func newChromeDragArea() *chromeDragArea {
	d := &chromeDragArea{}
	d.ExtendBaseWidget(d)
	return d
}

func (d *chromeDragArea) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

func (d *chromeDragArea) Dragged(*fyne.DragEvent) {
	// The OS move loop swallows the rest of the gesture (no DragEnd), so gate
	// on time instead of a per-gesture flag.
	if time.Since(d.lastDrag) < 300*time.Millisecond {
		return
	}
	d.lastDrag = time.Now()
	chromeStartDrag()
}

func (d *chromeDragArea) DragEnd() {}

func (d *chromeDragArea) DoubleTapped(*fyne.PointEvent) {
	if chromeEnabled {
		chromeToggleMax()
	}
}

// syncTabStrip shows the document tab strip only when there is more than one
// document (the mockup's clean blank state has no tab row).
func (ws *workspace) syncTabStrip() {
	if ws.tabStrip == nil || ws.tabs == nil {
		return
	}
	// Show the strip whenever at least one document is open, so the open
	// table/query name is visible in the content area (TablePlus-style). With
	// no documents the centre stays blank and the strip is hidden.
	if len(ws.tabs.tabs) > 0 {
		ws.tabStrip.Show()
	} else {
		ws.tabStrip.Hide()
	}
	if ws.body != nil {
		ws.body.Refresh()
	}
}

// bindDataFind wires the data grid's hit counter to the bottom-bar label.
func (ws *workspace) bindDataFind() {
	if ws.grid == nil || ws.findCounterLabel == nil {
		return
	}
	g := ws.grid
	lbl := ws.findCounterLabel
	g.onFindStatus = func(cur, total int) {
		if cur == 0 && total == 0 {
			lbl.SetText("")
		} else {
			lbl.SetText(fmt.Sprintf("%d/%d", cur, total))
		}
	}
}

func (ws *workspace) buildBottomBar() fyne.CanvasObject {
	// segment: Data | Structure | Indexes
	ws.segment = newSegmented([]string{"Data", "Structure", "Indexes"}, func(i int) {
		if ws.structPane == nil {
			return
		}
		if ws.activeData != nil {
			ws.activeData.segIdx = i // remember per tab which pane is showing
		}
		if i == 1 {
			ws.dataSlot.Hide()
			ws.structPane.Show()
			ws.indexPane.Hide()
		} else if i == 2 {
			ws.dataSlot.Hide()
			ws.structPane.Hide()
			ws.indexPane.Show()
			ws.loadIndexes()
		} else {
			ws.structPane.Hide()
			ws.indexPane.Hide()
			ws.dataSlot.Show()
		}
		ws.dataSlot.Refresh()
		ws.structPane.Refresh()
		ws.indexPane.Refresh()
	})

	// ---- row 1: 数据/结构/索引 segment + row actions (like a tab bar) ----
	addRow := newFlatButton("+ Row")
	addRow.onTap = func() { ws.showInsertRowDialog() }
	delRow := newFlatButton("− Row")
	delRow.onTap = func() { ws.markRowDeleted() }

	ws.dirtyLabel = widget.NewLabel("")
	ws.dirtyLabel.TextStyle.Italic = true
	ws.commitBtn = newFlatButton("Commit")
	ws.commitBtn.onTap = func() { ws.commitEdits() }
	ws.rollbackBtn = newFlatButton("Rollback")
	ws.rollbackBtn.onTap = func() { ws.rollbackEdits() }
	ws.previewBtn = newFlatButton("Preview")
	ws.previewBtn.onTap = func() { ws.previewEdits() }
	ws.dirtyLabel.Hide()
	ws.commitBtn.Hide()
	ws.rollbackBtn.Hide()
	ws.previewBtn.Hide()
	ws.txnRow = container.NewHBox(ws.dirtyLabel, spacerW(6), ws.rollbackBtn, spacerW(6),
		ws.previewBtn, spacerW(6), ws.commitBtn)

	row1 := container.NewHBox(container.NewPadded(ws.segment), spacerW(8),
		addRow, spacerW(6), delRow, spacerW(14), ws.txnRow)
	row1Box := newPanelBackground(container.NewPadded(row1),
		func() color.Color { return pal().toolbarBG })

	// ---- row 2: range text (left) + 筛选器/列 + paging (right) ----
	ws.prevBtn = newFlatButton("‹")
	ws.prevBtn.onTap = func() { ws.gotoOffset(ws.offset - browseLimit) }
	ws.nextBtn = newFlatButton("›")
	ws.nextBtn.onTap = func() { ws.gotoOffset(ws.offset + browseLimit) }
	// 加载更多值: a Redis collection continues in place instead of being
	// capped at its first window; hidden unless the pager says there is more.
	ws.moreValBtn = newFlatButton("加载更多值")
	ws.moreValBtn.onTap = func() { ws.loadMoreValueRows(ws.activeData) }
	ws.moreValBtn.Hide()
	pageMark := newFlatButton("≡")
	pageMark.onTap = func() { ws.showColumnsDialog() }
	ws.pageMarkBtn = pageMark
	filtersBtn := newFlatButton("Filter")
	filtersBtn.onTap = func() { ws.showFilterDialog() }
	ws.filterBtn = filtersBtn
	columnsBtn := newFlatButton("Columns")
	columnsBtn.onTap = func() { ws.showColumnsDialog() }
	ws.columnsBtn = columnsBtn
	// 删除键: a KV key's whole-key DEL, promoted from the sidebar right-click
	// menu to a visible action (hidden for SQL connections).
	ws.deleteKeyBtn = newFlatButton("删除键")
	ws.deleteKeyBtn.onTap = func() { ws.deleteActiveKey() }
	ws.deleteKeyBtn.Hide()
	row2right := container.NewHBox(filtersBtn, spacerW(6), columnsBtn, spacerW(10),
		ws.prevBtn, pageMark, ws.nextBtn, spacerW(6), ws.moreValBtn, spacerW(6), ws.deleteKeyBtn)
	// in-result search box (TablePlus in-grid spotlight) on the data tab.
	findEntry := newGridSearchEntry()
	findEntry.PlaceHolder = "在数据中查找…"
	ws.findCounterLabel = widget.NewLabel("")
	findEntry.OnChanged = func(s string) {
		if ws.grid != nil {
			ws.grid.setFindTerm(s)
		}
	}
	findEntry.onNext = func() {
		if ws.grid != nil {
			ws.grid.stepFind(1)
		}
	}
	findEntry.onPrev = func() {
		if ws.grid != nil {
			ws.grid.stepFind(-1)
		}
	}
	findBox := container.NewHBox(widget.NewIcon(theme.SearchIcon()), findEntry, ws.findCounterLabel)
	row2 := newPanelBackground(
		container.NewBorder(nil, nil, container.NewHBox(findBox, ws.status), row2right, layout.NewSpacer()),
		func() color.Color { return pal().toolbarBG })

	return container.NewVBox(widget.NewSeparator(), row1Box, row2)
}

// flatButton: a light bordered button like Columns/Filters.
type flatButton struct {
	widget.BaseWidget
	label   string
	enabled bool
	hover   bool
	onTap   func()
}

func newFlatButton(label string) *flatButton {
	b := &flatButton{label: label, enabled: true}
	b.ExtendBaseWidget(b)
	return b
}

func (b *flatButton) SetEnabled(e bool) {
	if b.enabled != e {
		b.enabled = e
		b.Refresh()
	}
}

func (b *flatButton) setLabel(s string) {
	if b.label != s {
		b.label = s
		b.Refresh()
	}
}

func (b *flatButton) Tapped(*fyne.PointEvent) {
	if b.enabled && b.onTap != nil {
		b.onTap()
	}
}
func (b *flatButton) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (b *flatButton) MouseIn(*desktop.MouseEvent)    { b.hover = true; b.Refresh() }
func (b *flatButton) MouseOut()                      { b.hover = false; b.Refresh() }
func (b *flatButton) MouseMoved(*desktop.MouseEvent) {}

func (b *flatButton) CreateRenderer() fyne.WidgetRenderer {
	// Ghost style: transparent by default, a soft fill on hover — no border.
	bg := canvas.NewRectangle(colorTransparent)
	bg.CornerRadius = 7
	text := canvas.NewText(b.label, pal().foreground)
	text.TextSize = 12
	return &flatBtnRenderer{b: b, bg: bg, text: text}
}

type flatBtnRenderer struct {
	b    *flatButton
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (r *flatBtnRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.text} }
func (r *flatBtnRenderer) MinSize() fyne.Size {
	return fyne.NewSize(r.text.MinSize().Width+18, 24)
}

func (r *flatBtnRenderer) Destroy() {}
func (r *flatBtnRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	t := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-t.Width)/2, (size.Height-t.Height)/2))
}
func (r *flatBtnRenderer) Refresh() {
	if r.text.Text != r.b.label {
		r.text.Text = r.b.label
	}
	p := pal()
	switch {
	case !r.b.enabled:
		r.bg.FillColor = colorTransparent
		r.text.Color = p.gridNullFG
	case r.b.hover:
		r.bg.FillColor = p.hover
		r.text.Color = p.foreground
	default:
		r.bg.FillColor = colorTransparent
		r.text.Color = p.foreground
	}
	r.bg.Refresh()
	r.text.Refresh()
}

// tab management ------------------------------------------------------------

// addDoc appends one strip entry of the given kind and returns its index.
// ws.docs stays parallel to ws.tabs.tabs so an index maps to its content.
func (ws *workspace) addDoc(kind docKind, title string, closeable bool, data *dataTableTab, qp *queryPane) int {
	ws.tabs.addTab(title, closeable)
	ws.docs = append(ws.docs, &docTab{kind: kind, data: data, qp: qp})
	return len(ws.docs) - 1
}

// docAt returns the document at strip index i (nil when out of range).
func (ws *workspace) docAt(i int) *docTab {
	if i < 0 || i >= len(ws.docs) {
		return nil
	}
	return ws.docs[i]
}

// dataDocIndex returns the strip index of the open table tab named name, or -1.
func (ws *workspace) dataDocIndex(name string) int {
	for i, d := range ws.docs {
		if d.kind == docData && d.data != nil && d.data.name == name {
			return i
		}
	}
	return -1
}

// removeDocAt drops strip index i from both the bar and the docs slice.
func (ws *workspace) removeDocAt(i int) {
	if i < 0 || i >= len(ws.docs) {
		return
	}
	ws.docs = append(ws.docs[:i], ws.docs[i+1:]...)
	ws.tabs.removeTab(i)
}

// removeCanvasObject returns objs without the first occurrence of o.
func removeCanvasObject(objs []fyne.CanvasObject, o fyne.CanvasObject) []fyne.CanvasObject {
	for i, x := range objs {
		if x == o {
			return append(objs[:i], objs[i+1:]...)
		}
	}
	return objs
}

// closeTab removes strip index i (a query pane or a table view) and switches to
// a surviving document. Every document tab is closeable.
func (ws *workspace) closeTab(i int) {
	doc := ws.docAt(i)
	if doc == nil {
		return
	}
	if doc.kind == docQuery && doc.qp != nil {
		for k, qp := range ws.queryTabs {
			if qp == doc.qp {
				ws.queryTabs = append(ws.queryTabs[:k], ws.queryTabs[k+1:]...)
				break
			}
		}
		ws.content.Objects = removeCanvasObject(ws.content.Objects, doc.qp)
	} else if doc.kind == docData && doc.data != nil {
		for k, t := range ws.dataTabs {
			if t == doc.data {
				ws.dataTabs = append(ws.dataTabs[:k], ws.dataTabs[k+1:]...)
				break
			}
		}
		if ws.activeData == doc.data {
			ws.activeData = nil
		}
	}
	ws.removeDocAt(i)
	if ws.tabs.current >= len(ws.docs) {
		ws.tabs.current = len(ws.docs) - 1
	}
	if ws.tabs.current < 0 {
		ws.tabs.current = 0
	}
	ws.tabs.setActive(ws.tabs.current)
	ws.showTab(ws.tabs.current)
	ws.syncTabStrip()
	if doc.kind == docData {
		// Closing a table tab discards its unsaved edits; the journal must not
		// resurrect them at the next launch. Synchronous flush: this is
		// teardown, a background task might not run before exit.
		ws.flushEditJournal()
	}
}

// saveActiveData persists the shared data-view state into the active table tab
// so switching away and back restores it exactly.
func (ws *workspace) saveActiveData() {
	t := ws.activeData
	if t == nil {
		return
	}
	t.name = ws.tableName
	t.grid = ws.grid
	t.cols = ws.tableCols
	t.offset = ws.offset
	t.totalRows = ws.totalRows
	t.totalApprox = ws.totalApprox
	t.filterSQL = ws.filterSQL
	t.loaded = ws.tableName != ""
}

// applyDataTab loads a table tab's state into the shared data-view widgets and
// makes it the active one. The bottom bar / details are single-instance, so
// they are re-synced from the tab on every switch.
func (ws *workspace) applyDataTab(t *dataTableTab) {
	ws.saveActiveData() // park the outgoing tab first
	ws.activeData = t
	ws.tableName = t.name
	ws.tableCols = t.cols
	ws.offset = t.offset
	ws.totalRows = t.totalRows
	ws.totalApprox = t.totalApprox
	ws.filterSQL = t.filterSQL

	// grid + structure/index panes are per-tab widgets; swap them in.
	if t.grid == nil {
		t.grid = newDataTable(emptyTableData(), ws.status)
		ws.prepGrid(t.grid)
		ws.bindDataFind()
	}
	ws.grid = t.grid
	ws.dataSlot.Objects = []fyne.CanvasObject{ws.grid}
	ws.dataSlot.Refresh()
	if t.structObj != nil {
		ws.structPane.Objects = []fyne.CanvasObject{t.structObj}
	}
	if t.indexObj != nil {
		ws.indexPane.Objects = []fyne.CanvasObject{t.indexObj}
	}
	ws.structPane.Refresh()
	ws.indexPane.Refresh()
	// KV keys are not tables: the Structure/Indexes segments are disguise
	// noise (synthetic columns, empty indexes), so the segmented control
	// hides and the view pins to Data. Switching back to a SQL connection
	// restores the normal chrome.
	if ws.kvMode() {
		if ws.segment != nil {
			ws.segment.Hide()
		}
		t.segIdx = 0
	} else if ws.segment != nil {
		ws.segment.Show()
	}
	ws.segment.set(t.segIdx)

	// re-sync the shared status/paging/filter chrome for this table.
	ws.status.setTable(t.name)
	ws.status.setColumns(len(t.cols))
	ws.status.setRows(t.gridRowCount(), t.totalRows, t.totalApprox)
	ws.status.setFrom(t.offset + 1)
	ws.updatePaging(t.offset > 0, t.gridRowCount() >= browseLimit)
	ws.refreshFilterChip()
	// KV keys have no WHERE filter and no column set to pick from: hide the
	// SQL-only chrome instead of letting it error or show noise on click.
	kv := ws.kvMode()
	if ws.filterBtn != nil {
		if kv {
			ws.filterBtn.Hide()
		} else {
			ws.filterBtn.Show()
		}
	}
	if ws.columnsBtn != nil {
		if kv {
			ws.columnsBtn.Hide()
		} else {
			ws.columnsBtn.Show()
		}
	}
	if ws.pageMarkBtn != nil {
		if kv {
			ws.pageMarkBtn.Hide()
		} else {
			ws.pageMarkBtn.Show()
		}
	}
	if ws.deleteKeyBtn != nil {
		// Only a KV key has a whole-key DEL; a SQL table is dropped, not
		// "deleted" row-for-row, so the button stays a KV-only affordance.
		if kv {
			ws.deleteKeyBtn.Show()
		} else {
			ws.deleteKeyBtn.Hide()
		}
	}
	ws.refreshRowDetails()
}

// gridRowCount reports how many rows a tab's grid currently shows.
func (t *dataTableTab) gridRowCount() int {
	if t == nil || t.grid == nil || t.grid.data == nil {
		return 0
	}
	return len(t.grid.data.Rows)
}

// newQueryTabPane builds one independent SQL tab: its own editor, result
// grid and find box. Each pane consults the workspace's live session, so
// switching databases never strands a captured (closed) connection.
func (ws *workspace) newQueryTabPane() *queryPane {
	qp := newQueryPane(ws.window, ws.conn, ws.sess, ws.side)
	qp.sessFn = func() db.Session { return ws.liveSession() }
	qp.onReconnect = ws.reconnect
	qp.onRowSelect = ws.showQueryRow
	qp.onLog = func(s string) { ws.logf("%s", s) }
	// A KV command can create/rename/delete keys: refresh the sidebar's key
	// inventory when the run lands, so the browse half of the dual mode stays
	// in step with the command half (suggestion 6).
	qp.onKeysChanged = ws.loadInventory
	qp.Hide()
	return qp
}

// loadHistoryIntoEditor puts a picked history statement into the active query
// editor (opening a query tab first if none is showing), ready to run or edit.
func (ws *workspace) loadHistoryIntoEditor(stmt string) {
	if doc := ws.docAt(ws.tabs.current); doc == nil || doc.kind != docQuery {
		ws.addQueryTab()
	}
	if qp := ws.activeQueryPane(); qp != nil {
		qp.setEditorTextAtEnd(stmt)
	}
}

// activeQueryPane returns the query pane for the current tab, or the last one
// if no tab is active. Used by the quick-open palette to fill an editor.
func (ws *workspace) activeQueryPane() *queryPane {
	if doc := ws.docAt(ws.tabs.current); doc != nil && doc.kind == docQuery {
		return doc.qp
	}
	if len(ws.queryTabs) > 0 {
		return ws.queryTabs[len(ws.queryTabs)-1]
	}
	return nil
}

func (ws *workspace) addQueryTab() {
	title := "SQL Query"
	if n := len(ws.queryTabs) + 1; n > 1 {
		title = fmt.Sprintf("SQL Query-%d", n)
	}
	qp := ws.addQueryTabTitled(title, "")
	_ = qp
}

// addQueryTabTitled opens a new editor tab with the given title and optional
// pre-filled SQL (used by the DDL route). Returns the pane.
func (ws *workspace) addQueryTabTitled(title, sql string) *queryPane {
	qp := ws.newQueryTabPane()
	ws.queryTabs = append(ws.queryTabs, qp)
	ws.content.Objects = append(ws.content.Objects, qp)
	idx := ws.addDoc(docQuery, title, true, nil, qp)
	ws.tabs.setActive(idx)
	ws.showTab(idx)
	ws.syncTabStrip()
	if sql != "" {
		safeDo(func() {
			if qp.editor != nil {
				qp.editor.SetText(sql)
			}
		})
	}
	return qp
}

// openObjectByName opens the picked sidebar object, routing by kind:
// functions open as an editable DDL tab, tables/views open in the data tab.
func (ws *workspace) openObjectByName(name string) {
	if ws.side != nil {
		for _, o := range ws.side.objects {
			if o.Name == name && o.Kind == "function" {
				ws.openObjectDDL(name, "function")
				return
			}
		}
	}
	ws.openTable(name)
}

// openObjectDDL fetches an object's DDL and opens it in a new editor tab
// (the "pure DDL route" — the user can read, copy or adapt the statement).
func (ws *workspace) openObjectDDL(name, kind string) {
	if ws.sess == nil {
		return
	}
	sess := ws.sess
	driver := ws.conn.Driver
	gen := ws.nextGen()
	safeGo("openDDL "+name, func() {
		text, err := fetchObjectDDL(sess, driver, kind, name)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "DDL "+name, "Could not read the DDL:\n"+err.Error())
				return
			}
			ws.addQueryTabTitled("DDL · "+name, text)
		})
	})
}

// ---- saved queries (侧栏 查询 标签) ----------------------------------------

// openSavedQuery drops a saved query's SQL into the active editor and, when
// runNow is set, executes it right away.
func (ws *workspace) openSavedQuery(q SavedQuery, runNow bool) {
	qp := ws.activeQueryPane()
	if qp == nil {
		ws.addQueryTab()
		qp = ws.activeQueryPane()
	}
	if qp == nil {
		return
	}
	if idx := ws.queryTabIndex(qp); idx >= 0 {
		ws.tabs.setActive(idx)
		ws.showTab(idx)
	}
	sql := q.SQL
	safeDo(func() {
		if qp.editor != nil {
			qp.editor.SetText(sql)
		}
	})
	if runNow {
		// Give the editor text a beat to land, then run.
		safeGo("run-saved-query", func() {
			time.Sleep(50 * time.Millisecond)
			safeDo(func() {
				if qp.editor != nil && strings.TrimSpace(qp.editor.Text) == strings.TrimSpace(sql) {
					qp.run()
				}
			})
		})
	}
}

// saveCurrentQuery prompts for a name and (optional) keywords, then stores
// the active editor's content in the saved-query panel.
func (ws *workspace) saveCurrentQuery() {
	qp := ws.activeQueryPane()
	sql := ""
	if qp != nil && qp.editor != nil {
		sql = strings.TrimSpace(qp.editor.Text)
	}
	if sql == "" {
		showInfo(ws.window, "保存查询", "当前编辑器为空，没有可保存的内容。")
		return
	}
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("查询名称")
	nameEntry.SetText("查询 " + fmt.Sprint(time.Now().Format("15:04")))
	kwEntry := widget.NewEntry()
	kwEntry.SetPlaceHolder("关键字（逗号分隔，可留空）")

	var dlg dialog.Dialog
	saveBtn := widget.NewButton("保存", func() {
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			showInfo(ws.window, "保存查询", "请填写查询名称。")
			return
		}
		kws := strings.Split(kwEntry.Text, ",")
		keywords := make([]string, 0, len(kws))
		for _, k := range kws {
			if k = strings.TrimSpace(k); k != "" {
				keywords = append(keywords, k)
			}
		}
		if err := upsertSavedQuery(SavedQuery{Name: name, Keywords: keywords, SQL: sql}); err != nil {
			dialog.NewError(err, ws.window).Show()
			return
		}
		dlg.Hide()
		ws.side.refreshQueries()
		showInfo(ws.window, "保存查询", "已保存到「查询」列表。")
	})
	saveBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("取消", func() { dlg.Hide() })

	body := container.NewBorder(
		container.NewVBox(
			widget.NewLabel("名称"),
			nameEntry,
			widget.NewLabel("关键字"),
			kwEntry,
		),
		container.NewHBox(layout.NewSpacer(), cancelBtn, saveBtn),
		nil, nil,
		nil,
	)
	dlg = dialog.NewCustom("保存查询", "", body, ws.window)
	dlg.Resize(fyne.NewSize(420, 240))
	dlg.Show()
}

// showQueryMenu pops the context menu for a saved-query row.
func (ws *workspace) showQueryMenu(q SavedQuery, pos fyne.Position) {
	if ws.body == nil {
		return
	}
	canvas := fyne.CurrentApp().Driver().CanvasForObject(ws.body)
	if canvas == nil {
		return
	}
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("运行", func() { ws.openSavedQuery(q, true) }),
		fyne.NewMenuItem("在编辑器中打开", func() { ws.openSavedQuery(q, false) }),
		fyne.NewMenuItem("复制 SQL", func() {
			fyne.CurrentApp().Clipboard().SetContent(q.SQL)
			showInfo(ws.window, "复制", "已复制 "+q.Name+" 的 SQL。")
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("删除", func() {
			dialog.NewConfirm("删除查询",
				fmt.Sprintf("删除已保存的查询 %q？数据库本身不受影响。", q.Name),
				func(ok bool) {
					if !ok {
						return
					}
					if err := deleteSavedQuery(q.Name); err != nil {
						dialog.NewError(err, ws.window).Show()
						return
					}
					ws.side.refreshQueries()
				}, ws.window).Show()
		}),
	)
	widget.ShowPopUpMenuAtPosition(menu, canvas, pos)
}

// syncChrome shows/hides the data-view bottom bar. When no table is open on
// the data tab the centre stays blank (mockup), and query tabs keep their own
// editor/status chrome.
func (ws *workspace) syncChrome() {
	if ws.bottomBar == nil {
		return
	}
	doc := ws.docAt(ws.tabs.current)
	dataTab := doc != nil && doc.kind == docData
	hasTable := dataTab && ws.tableName != ""
	if dataTab && hasTable {
		ws.bottomBar.Show() // data tab: 数据/结构/索引 + paging
	} else {
		ws.bottomBar.Hide() // blank data tab or query tab: editor + results only
	}
	// Keep the centre blank until a table is opened (the empty grid would
	// otherwise paint its row-number gutter in the middle of the mockup).
	if ws.dataStack != nil {
		if dataTab && hasTable {
			ws.dataStack.Show()
		} else {
			ws.dataStack.Hide()
		}
	}
	if ws.body != nil {
		ws.body.Refresh()
	}
}

// showTab switches the visible content to the document at strip index i: a
// table view (data stack, re-synced from that tab's stored state) or an
// independent query pane. Everything else is hidden.
func (ws *workspace) showTab(i int) {
	ws.dataStack.Hide()
	for _, qp := range ws.queryTabs {
		qp.hideAuto() // the popup is a canvas overlay, not clipped by the hidden pane
		qp.Hide()
	}
	doc := ws.docAt(i)
	if doc != nil {
		switch doc.kind {
		case docData:
			if doc.data != nil && ws.activeData != doc.data {
				ws.applyDataTab(doc.data)
			}
			ws.dataStack.Show()
		case docQuery:
			if doc.qp != nil {
				doc.qp.Show()
			}
		}
	}
	ws.syncChrome()
	ws.refreshRibbon()
	ws.content.Refresh()
}

// closeDataTabNamed closes the data tab named name (a deleted or renamed key),
// reporting whether such a tab was open.
func (ws *workspace) closeDataTabNamed(name string) bool {
	idx := ws.dataDocIndex(name)
	if idx < 0 {
		return false
	}
	ws.closeTab(idx)
	return true
}

// openTable opens a table (or view) in a document tab. A table that already
// has a tab is brought to the front with its grid/paging restored; otherwise a
// new closable tab is created and its first page loaded.
func (ws *workspace) openTable(name string) {
	if idx := ws.dataDocIndex(name); idx >= 0 {
		ws.tabs.setActive(idx)
		ws.showTab(idx)
		ws.syncTabStrip()
		return
	}
	t := &dataTableTab{name: name}
	ws.dataTabs = append(ws.dataTabs, t)
	idx := ws.addDoc(docData, name, true, t, nil)
	ws.tabs.setActive(idx)
	ws.showTab(idx) // applies the tab: placeholder grid + chrome
	ws.syncTabStrip()
	ws.updateBanner()
	ws.logf("打开表 %s …", name)
	ws.loadTableData(t, 0)
}

// openTableFiltered opens (or reuses) a table tab and applies a goto filter,
// reloading its first page. Used by the cross-table "goto row" jump, where the
// filter is intentional and must survive the tab switch.
func (ws *workspace) openTableFiltered(name, filter string) {
	idx := ws.dataDocIndex(name)
	if idx < 0 {
		t := &dataTableTab{name: name, filterSQL: filter}
		ws.dataTabs = append(ws.dataTabs, t)
		idx = ws.addDoc(docData, name, true, t, nil)
	}
	ws.tabs.setActive(idx)
	ws.showTab(idx)
	ws.syncTabStrip()
	if doc := ws.docAt(idx); doc != nil && doc.data != nil {
		doc.data.filterSQL = filter
	}
	ws.filterSQL = filter
	ws.refreshFilterChip()
	ws.updateBanner()
	ws.reloadActiveData()
}

// tabOpen reports whether t is still an open document tab.
func (ws *workspace) tabOpen(t *dataTableTab) bool {
	for _, x := range ws.dataTabs {
		if x == t {
			return true
		}
	}
	return false
}

// pushTabGrid installs a tab's grid (and paging/status) into the shared data
// view when that tab is the active one.
func (ws *workspace) pushTabGrid(t *dataTableTab) {
	if ws.activeData != t || t.grid == nil {
		return
	}
	ws.grid = t.grid
	ws.bindDataFind()
	ws.dataSlot.Objects = []fyne.CanvasObject{ws.grid}
	ws.dataSlot.Refresh()
	ws.status.setTable(t.name)
	ws.status.setColumns(len(t.cols))
	ws.status.setRows(t.gridRowCount(), t.totalRows, t.totalApprox)
	ws.status.setFrom(t.offset + 1)
	ws.updatePaging(t.offset > 0, t.gridRowCount() >= browseLimit)
	ws.refreshRowDetails()
}

// loadTableData fetches one page of a tab's table in the background and stores
// the resulting grid on the tab, pushing it to the shared view only if the tab
// is still open and active when the result lands.
func (ws *workspace) loadTableData(t *dataTableTab, offset int) {
	sess := ws.liveSession()
	if sess == nil || t == nil {
		return
	}
	// FQ-02: a transparently rebuilt plugin session re-opened the server
	// connection — server-side state from before the rebuild is gone. The
	// notice fires once (acknowledge-on-read), right where data is re-read.
	if o, ok := db.AsRebuildObserver(sess); ok && o.TakeRebuilt() {
		ws.logf("插件进程已重建：连接已重新打开，此前的事务/临时表/会话变量已丢失")
		ws.status.setQueryInfo("⚠ 插件进程已重建——服务器端会话状态已丢失")
	}
	name := t.name
	filter := t.filterSQL
	t.gen++
	gen := t.gen
	// Snapshot the driver before the goroutine: ws.conn is swapped by
	// switchDatabase/reconnect on the UI thread, and reading it inside
	// safeGo races with that write.
	driver := ws.conn.Driver
	safeGo("loadTable "+name, func() {
		var res proto.QueryResult
		var err error
		kvNext := ""
		if strings.TrimSpace(filter) == "" {
			// A Redis collection reads its first window through the kernel's
			// value pager when the driver has it: the bounded whole read knew
			// only "truncated", never how to continue — a million-member key
			// was its first 300 rows forever.
			if driver == "redis" {
				if pager, pok := db.AsValuePager(sess); pok {
					res, _, kvNext, _, err = pager.BrowseValuePage(name, "", browseLimit)
				} else {
					res, err = sess.Browse(name, browseLimit)
				}
			} else {
				res, err = sess.Browse(name, browseLimit)
			}
		} else if driver == "redis" {
			// A Redis key has no WHERE: the filter dialog must not have let a
			// filter through (showFilterDialog guards on pageSupported), so a
			// filter reaching here is a state the UI can no longer produce —
			// report it instead of leaving as an SQL statement to a driver
			// that parses Redis commands.
			err = fmt.Errorf("Redis 键不支持筛选，请清除筛选后浏览")
		} else {
			res, err = sess.Query(
				fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT %d",
					ws.quoteTable(name), filter, browseLimit), browseLimit)
		}
		safeDo(func() {
			if ws.liveSession() != sess || gen != t.gen || !ws.tabOpen(t) {
				return
			}
			if err != nil {
				ws.logf("打开表 %s 失败：%v", name, err)
				if ws.activeData == t {
					showInfo(ws.window, "Load "+name, err.Error())
				}
				return
			}
			t.kvNext = kvNext
			data := tableDataFromResult(res, -1, false)
			t.totalRows = data.Fetched
			t.totalApprox = false
			if res.Truncated {
				// A bounded read (Redis value pages) says "this is a window":
				// the status bar keeps the ~ marker and the log carries the
				// detail, so the first page never poses as the whole key.
				t.totalApprox = true
				ws.logf("键 %s 的值超过单页上限，仅显示前 %d 行", name, len(data.Rows))
			}
			t.offset = offset
			t.loaded = true
			grid := newDataTable(data, ws.status)
			ws.prepGrid(grid)
			t.grid = grid
			ws.pushTabGrid(t)
			ws.logf("已加载表 %s：%d 行", name, len(data.Rows))
			if driver != "redis" {
				// A KV key has synthetic columns and no indexes: both panes
				// would be disguise noise (and two wasted round trips).
				ws.loadStructureTab(t, sess)
				ws.loadIndexesTab(t, sess)
			} else if ka, ok := db.AsKeyAdmin(sess); ok {
				// 键视图 metadata: the details pane shows the key's type and
				// remaining TTL while no element row is selected — the KV
				// answer to a SQL table's column structure.
				if meta, merr := ka.KeyMeta(name); merr == nil {
					safeDo(func() {
						if ws.liveSession() != sess || gen != t.gen || !ws.tabOpen(t) {
							return
						}
						t.kvMeta = &meta
						ws.refreshRowDetails()
					})
				}
			}
			safeGo("approxRows "+name, func() {
				total, approx := ws.approxRows(name)
				safeDo(func() {
					if ws.liveSession() != sess || gen != t.gen || !ws.tabOpen(t) {
						return
					}
					t.totalRows = total
					t.totalApprox = approx
					if ws.activeData == t {
						ws.status.setRows(t.gridRowCount(), total, approx)
					}
				})
			})
		})
	})
}

// loadMoreValueRows appends the next window of a Redis collection to the open
// grid: the pager continues from t.kvNext, rows land in place via
// appendRows (no tab reload), and the button hides when the walk is over.
func (ws *workspace) loadMoreValueRows(t *dataTableTab) {
	if t == nil || t.kvNext == "" {
		return
	}
	sess := ws.liveSession()
	if sess == nil {
		return
	}
	pager, ok := db.AsValuePager(sess)
	if !ok {
		return
	}
	name := t.name
	cursor := t.kvNext
	t.gen++
	gen := t.gen
	safeGo("moreValues "+name, func() {
		_, rows, next, more, err := pager.BrowseValuePage(name, cursor, browseLimit)
		safeDo(func() {
			if ws.liveSession() != sess || gen != t.gen || !ws.tabOpen(t) {
				return
			}
			if err != nil {
				ws.logf("加载更多值失败：%v", err)
				if ws.activeData == t {
					showInfo(ws.window, "加载更多值", err.Error())
				}
				return
			}
			if t.grid == nil {
				return
			}
			t.grid.appendRows(rows)
			t.kvNext = next
			if ws.activeData == t {
				ws.status.setRows(t.gridRowCount(), t.totalRows, true)
				// 新追加的行也进右侧行详情的可见集（对齐 loadTableData 的
				// 刷新口径）；总数未知时状态栏按 total<0 渲染，不再出 "-1"。
				ws.refreshRowDetails()
			}
			ws.updatePaging(t.offset > 0, t.gridRowCount() >= browseLimit)
			_ = more
		})
	})
}

// loadTablePage loads a specific page (paging ‹/›) for a tab.
func (ws *workspace) loadTablePage(t *dataTableTab, off int) {
	sess := ws.liveSession()
	if sess == nil || t == nil {
		return
	}
	// Only SQL drivers speak OFFSET/LIMIT. The paging buttons are hidden for
	// the others (pageSupported gates the toolbar), so reaching here is a
	// state the UI can no longer produce; the guard keeps a Redis key from
	// ever leaving as a SELECT statement.
	if !ws.pageSupported() {
		return
	}
	name := t.name
	t.gen++
	gen := t.gen
	safeGo("page "+name, func() {
		sql := fmt.Sprintf("SELECT * FROM %s%s%s",
			ws.quoteTable(name), ws.whereClause(), ws.pageSuffix(browseLimit+1, off))
		res, err := sess.Query(sql, browseLimit+1)
		safeDo(func() {
			if ws.liveSession() != sess || gen != t.gen || !ws.tabOpen(t) {
				return
			}
			if err != nil {
				if ws.activeData == t {
					showInfo(ws.window, "加载 "+name, err.Error())
				}
				return
			}
			hasMore := len(res.Rows) > browseLimit
			if hasMore {
				res.Rows = res.Rows[:browseLimit]
			}
			data := tableDataFromResult(res, t.totalRows, t.totalApprox)
			t.offset = off
			grid := newDataTable(data, ws.status)
			ws.prepGrid(grid)
			old := t.grid
			t.grid = grid
			// Paging no longer discards the old page's unsaved edits: they are
			// re-applied in place when the incoming page shows their row and
			// ride the tab's carry-over otherwise (commit/preview/banner/
			// journal all merge it). The rewrite also keeps the journal in
			// step with whatever survived the flip.
			ws.carryPageEdits(t, old, grid)
			ws.saveEditJournal()
			if ws.activeData == t {
				ws.pushTabGrid(t)
				ws.updatePaging(off > 0, hasMore)
			}
			// Completion sequence: bumped after every mutation this callback
			// makes (grid swap, carry re-application, journal snapshot).
			// Observers that read tab state from another goroutine — tests
			// under -race, and any future UI sync point — wait on it instead
			// of polling unsynchronized fields.
			t.pageDone.Add(1)
		})
	})
}

// loadStructureTab fetches column metadata for a tab's Structure pane.
func (ws *workspace) loadStructureTab(t *dataTableTab, sess db.Session) {
	name := t.name
	gen := t.gen
	safeGo("loadStructure "+name, func() {
		cols, err := sess.Columns(name)
		safeDo(func() {
			if gen != t.gen || !ws.tabOpen(t) {
				return
			}
			data := &TableData{}
			if err == nil {
				for _, c := range cols {
					data.Cols = append(data.Cols, ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Key: c.Key})
				}
			}
			t.cols = data.Cols
			t.structObj = newStructurePane(data)
			if ws.activeData == t {
				ws.tableCols = t.cols
				ws.structPane.Objects = []fyne.CanvasObject{t.structObj}
				ws.structPane.Hide()
				ws.structPane.Refresh()
				ws.status.setColumns(len(t.cols))
				ws.refreshRowDetails()
			}
			if err != nil && ws.activeData == t {
				showInfo(ws.window, "Structure "+name, err.Error())
			}
		})
	})
}

// loadIndexesTab fetches index metadata for a tab's Indexes pane. Key/value
// sessions have no SQL indexes, and asking would just log a server error.
func (ws *workspace) loadIndexesTab(t *dataTableTab, sess db.Session) {
	if _, ok := db.AsKeyAdmin(sess); ok {
		return
	}
	name := t.name
	gen := t.gen
	// Snapshot the database before the goroutine: ws.conn is swapped by
	// switchDatabase/reconnect on the UI thread, and reading it inside
	// safeGo races with that write.
	curDB := ws.conn.Database
	safeGo("indexes "+name, func() {
		idx := fetchIndexes(sess, curDB, name)
		safeDo(func() {
			if gen != t.gen || !ws.tabOpen(t) {
				return
			}
			if idx != nil {
				t.indexObj = newListPane([]string{"Name", "Column", "Unique", "Type"}, indexRows(idx))
				if ws.activeData == t {
					ws.indexPane.Objects = []fyne.CanvasObject{t.indexObj}
					ws.indexPane.Hide()
					ws.indexPane.Refresh()
				}
			}
		})
	})
}

// reloadActiveData re-queries the active table tab's first page.
func (ws *workspace) reloadActiveData() {
	t := ws.activeData
	if t == nil || t.name == "" {
		return
	}
	ws.loadTableData(t, 0)
}

// closeAllDataTabs drops every open table tab (used when the database or
// session changes, stranding their grids).
func (ws *workspace) closeAllDataTabs() {
	for i := len(ws.docs) - 1; i >= 0; i-- {
		if ws.docs[i].kind == docData {
			ws.removeDocAt(i)
		}
	}
	ws.dataTabs = nil
	ws.activeData = nil
	ws.tableName = ""
	ws.tableCols = nil
	if ws.tabs.current >= len(ws.docs) {
		ws.tabs.current = len(ws.docs) - 1
	}
	if ws.tabs.current < 0 {
		ws.tabs.current = 0
	}
	ws.tabs.setActive(ws.tabs.current)
	ws.syncTabStrip()
}

// indexRows reshapes a fetched index result (Name/Seq/Column/Unique/Type)
// into display rows; rows arrive ordered by (INDEX_NAME, SEQ_IN_INDEX), so
// composite-key columns are appended in order.
func indexRows(data *TableData) [][]string {
	byName := map[string][]string{} // name -> [name, unique, type, cols...]
	var order []string
	for _, r := range data.Rows {
		cell := func(i int) string {
			if i < len(r) && r[i] != nil {
				return *r[i]
			}
			return ""
		}
		name := cell(0)
		if _, ok := byName[name]; !ok {
			byName[name] = []string{name, cell(3), cell(4)}
			order = append(order, name)
		}
		byName[name] = append(byName[name], cell(2))
	}
	rows := make([][]string, 0, len(order))
	for _, name := range order {
		e := byName[name]
		rows = append(rows, []string{e[0], strings.Join(e[3:], ", "), e[1], e[2]})
	}
	return rows
}

// ---------------------------------------------------------------------------
// paging

func (ws *workspace) pageSupported() bool {
	switch ws.conn.Driver {
	case "mysql", "mariadb", "postgres", "postgresql", "mssql", "sqlserver", "oracle", "sqlite":
		return true
	}
	return false
}

// pageSuffix renders the dialect's paging tail. T-SQL has no LIMIT/OFFSET; its
// OFFSET/FETCH requires an ORDER BY, which the synthetic (SELECT NULL)
// provides when the user did not pick one. Oracle 12c+ shares the OFFSET/FETCH
// shape (no ORDER BY needed). Everything else here speaks LIMIT/OFFSET. Before
// this existed, a filtered browse on MSSQL emitted LIMIT and failed with a
// syntax error every time.
func (ws *workspace) pageSuffix(limit, offset int) string {
	switch ws.conn.Driver {
	case "mssql", "sqlserver":
		return fmt.Sprintf(" ORDER BY (SELECT NULL) OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	case "oracle":
		return fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	default:
		return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
}

func (ws *workspace) quoteTable(name string) string {
	// Single-sourced with the rest of the client's identifier quoting: the
	// hand-rolled switch here missed SQL Server, where ANSI double quotes are
	// only an identifier when QUOTED_IDENTIFIER is on (the driver default),
	// and could not be trusted once it was off. QuoteIdentLoose picks the
	// dialect's own delimiters and falls back to ANSI for unknown drivers.
	if q, err := sqltext.QuoteIdent(ws.conn.Driver, name); err == nil {
		return q
	}
	return sqltext.QuoteIdentLoose(ws.conn.Driver, name)
}

// quoteSQLString renders v as a single-quoted SQL string literal for the
// connection's dialect. MySQL (unless NO_BACKSLASH_ESCAPES) treats '\' as an
// escape inside literals, so a value ending in a backslash would escape the
// closing quote and corrupt the statement — double it along with the quotes.
func (ws *workspace) quoteSQLString(v string) string {
	if ws.conn.Driver == "mysql" || ws.conn.Driver == "mariadb" {
		v = strings.ReplaceAll(v, `\`, `\\`)
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// updatePaging refreshes the ‹/› button states.
func (ws *workspace) updatePaging(canPrev, canNext bool) {
	if ws.prevBtn == nil {
		return
	}
	if !ws.pageSupported() {
		canPrev, canNext = false, false
	}
	ws.prevBtn.SetEnabled(canPrev)
	ws.nextBtn.SetEnabled(canNext)
	if ws.moreValBtn != nil {
		// The Redis value walk is append-only: the button's visibility IS the
		// pager's "more" answer (kvNext ""), driven by the active tab.
		if t := ws.activeData; t != nil && t.kvNext != "" {
			ws.moreValBtn.Show()
		} else {
			ws.moreValBtn.Hide()
		}
	}
}

// gotoOffset loads one page of the active table starting at row off.
func (ws *workspace) gotoOffset(off int) {
	t := ws.activeData
	if t == nil || t.name == "" || ws.sess == nil {
		return
	}
	// A driver without SQL paging cannot serve a filtered page either: the
	// filter would reach loadTablePage and leave as a SELECT … LIMIT … OFFSET
	// statement to a driver that parses Redis commands. Redis keys have no
	// offset paging and no WHERE, so the guard covers both shapes at once.
	if !ws.pageSupported() {
		return
	}
	if off < 0 {
		off = 0
	}
	if off == t.offset {
		return
	}
	ws.loadTablePage(t, off)
}

// loadIndexes refreshes the Indexes pane for the active table tab (reusing the
// cached pane when one was already fetched for this tab).
func (ws *workspace) loadIndexes() {
	t := ws.activeData
	if t == nil || t.name == "" {
		return
	}
	if t.indexObj != nil {
		ws.indexPane.Objects = []fyne.CanvasObject{t.indexObj}
		ws.indexPane.Refresh()
		return
	}
	ws.loadIndexesTab(t, ws.liveSession())
}

// cachedInventory returns the catalog loadInventory fetched, or nil when none
// is loaded. UI-thread callers must not fall back to a live Tables() — that
// would put a network round-trip back on the UI thread.
func (ws *workspace) cachedInventory() []proto.ObjectInfo {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	return ws.invCache
}

// approxRows looks up the inventory entry for the current table in the
// catalog cached by loadInventory. It used to re-pull Tables() on every table
// open — a full catalog round-trip per click (N+1 when browsing several
// tables). A cold cache (inventory not loaded yet) simply reports "unknown".
func (ws *workspace) approxRows(name string) (int, bool) {
	ws.loadingMu.Lock()
	objs := ws.invCache
	ws.loadingMu.Unlock()
	for _, o := range objs {
		if o.Name == name {
			return int(o.Rows), o.ApproxRows && o.Rows >= 0
		}
	}
	return -1, false
}

func (ws *workspace) reloadTable() {
	if ws.activeData == nil || ws.activeData.name == "" {
		ws.loadInventory()
		return
	}
	ws.reloadActiveData()
}
