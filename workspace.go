package main

// The workspace window: toolbar with connection breadcrumb, document tabs,
// sidebar tree, data grid / SQL editor, bottom segmented control + status
// bar. All data comes from the live db.Session handed to openWorkspace.

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
	"fynequery/proto"
)

// browseLimit is how many rows a table data tab loads (one page).
const browseLimit = 300

type workspace struct {
	window           fyne.Window
	conn             Connection
	sess             db.Session
	dbType           DBType
	side             *sidebar
	tabs             *tabBar
	content          *fyne.Container // stacked tab contents
	status           *statusBar
	segment          *segmented
	structPane       *fyne.Container
	indexPane        *fyne.Container   // 索引 segment content
	dataStack        *fyne.Container   // dataSlot + struct + index (tab 0 content)
	queryTabs        []*queryPane      // one independent query pane per SQL tab
	details          *detailsPane      // right sidebar
	detailWrap       fyne.CanvasObject // toggled by the toolbar button
	crumbBox         *crumbBox         // rounded table-name field
	currentDB        string            // selected database ("" = none yet)
	dataSlot         *fyne.Container   // holds the current data grid (replaced per table)
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
	crumbBar         fyne.CanvasObject // top nav row (toggled per tab)
	bottomBar        fyne.CanvasObject // data-view bottom bar (hidden in query tabs)
	pendingDDL       []string          // queued ALTER TABLE statements (Preview/Commit)
	findCounterLabel *widget.Label     // "cur/total" counter for the data-grid find box
	loadingMu        sync.Mutex
	loadingGen       int // generation counter to discard stale async results
	// toolbar
	ribbon        *serverRibbon     // green server-info ribbon (opens connection picker)
	ribbonTLS     string            // TLS version shown in the ribbon ("" = omit)
	dbSelect      *crumbPill        // database selector pill (opens 打开数据库)
	sideWrap      fyne.CanvasObject // left sidebar wrapper (toggled by the toolbar)
	body          fyne.CanvasObject // root container (refreshed after panel toggles)
	welcomeReturn func()            // brings the welcome window back after close
	skipWelcome   bool              // suppress welcomeReturn on the next close
}

// openWorkspace dials nothing: the caller passes an established session.
// onClose (optional) is invoked after the workspace window closes — used to
// bring the welcome page back, like TablePlus.
func openWorkspace(app fyne.App, conn Connection, sess db.Session, dark bool, onClose func()) {
	darkMode = dark
	w := app.NewWindow("TablePro — " + conn.Name)
	w.Resize(fyne.NewSize(1280, 800))

	// TablePlus opens at the server level with no database selected. Re-dial
	// without a default database so the workspace starts in the "未选择数据库"
	// state; fall back to the given (database-bound) session if that fails
	// (e.g. drivers that require a database to connect).
	if conn.Database != "" {
		serverConn := conn
		serverConn.Database = ""
		if ss, err := dialConn(serverConn); err == nil {
			if sess != nil {
				sess.Close()
			}
			sess = ss
			conn = serverConn
		}
	}

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
		if ws.sess != nil {
			ws.sess.Close()
			ws.sess = nil
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
	// Ctrl+K opens the quick-open palette (TablePlus OpenAnything). The
	// database picker remains reachable from the toolbar database pill.
	w.Canvas().AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyK, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) { ws.showQuickOpen() })
	w.Show()
	// Refresh the theme only now that the window has a native handle; pushing
	// the app theme earlier would make Fyne walk every window's HWND for
	// dark-mode and panic on any that is not yet shown.
	applyTheme(w)
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
func (ws *workspace) updateBanner() {
	if ws.dbSelect != nil {
		name := ws.currentDB
		if name == "" {
			name = "选择数据库"
		}
		ws.dbSelect.setText(name)
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
	if ws.tabs == nil || ws.tabs.current == 0 {
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
func (ws *workspace) openDatabasePicker() {
	if ws.sess == nil {
		return
	}
	showOpenDatabaseDialog(ws.window, ws.sess, ws.conn.Driver, ws.currentDB, func(name string) {
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
		fyne.NewMenuItem(fmt.Sprintf("保活 ping(30s): %s", map[bool]string{true: "开", false: "关"}[keepAliveEnabled]), func() {
			keepAliveEnabled = !keepAliveEnabled
			showInfo(ws.window, "连接保活",
				fmt.Sprintf("每 30 秒向服务器发一次 ping，防止空闲连接被防火墙/SSH 断开。当前：%s。",
					map[bool]string{true: "开", false: "关"}[keepAliveEnabled]))
		}),
		fyne.NewMenuItem(privacyLabel, func() {
			on := togglePrivacyMode()
			ws.refreshRibbon()
			showInfo(ws.window, "隐私模式",
				fmt.Sprintf("隐私模式已%s。\n主机/端口/库名在界面中显示为 •••。", map[bool]string{true: "开启", false: "关闭"}[on]))
		}),
		fyne.NewMenuItem("ER Diagram", func() { ws.openERDiagram() }),
		fyne.NewMenuItem("User Management", func() { ws.openUserManagement() }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("侧边栏 显示/隐藏", func() { ws.toggleSidebar() }),
		fyne.NewMenuItem("详情面板 显示/隐藏", func() { ws.toggleDetails() }),
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
	if ws.body != nil {
		ws.body.Refresh()
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
	sess, err := dialConn(newConn)
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
	ws.tableName = ""
	ws.updateBanner()
	ws.syncChrome() // blank center until a table is opened
	ws.loadInventory()
	ws.loadDatabases()
	ws.loadTLS()
}

// sideWidth mirrors TablePlus' sidebar column (~18% of the window, measured
// 352px in a 1915px live window).
const sideWidth float32 = 236

// detailWidth is the initial width of the right 详情/助理 pane. Both panels are
// resizable afterwards by dragging their divider.
const detailWidth float32 = 280

// keep-alive + reconnect helpers (TablePlus "keep connection alive").

// keepAliveInterval is how often the live session is pinged so idle sockets
// (closed by servers/firewalls/SSH) stay open.
const keepAliveInterval = 30 * time.Second

var keepAliveEnabled = true // toggled from the 更多功能 menu

// liveSession / setSession guard ws.sess against the keep-alive goroutine.
func (ws *workspace) liveSession() db.Session {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	return ws.sess
}

func (ws *workspace) setSession(s db.Session) {
	ws.loadingMu.Lock()
	defer ws.loadingMu.Unlock()
	ws.sess = s
}

// startKeepAlive pings the session every 30s in the background. It reads the
// current session pointer each tick (so a database switch just follows), and
// is a no-op while keepAliveEnabled is off. SQLite connections (file-based,
// never idle-closed) skip the ping.
func (ws *workspace) startKeepAlive() {
	if ws.conn.Driver == "sqlite" {
		return
	}
	go func() {
		tick := time.NewTicker(keepAliveInterval)
		defer tick.Stop()
		for range tick.C {
			if !keepAliveEnabled {
				continue
			}
			sess := ws.liveSession()
			if sess == nil {
				continue
			}
			// A successful ping keeps the socket open; errors are swallowed
			// (the next tick retries). SELECT 1 is universal across drivers.
			safeGo("keepalive", func() {
				_, _ = sess.Query("SELECT 1", 1)
			})
		}
	}()
}

// reconnect dials a fresh session for the workspace's connection and swaps it
// in (the timeout dialog's "Reconnect & Retry" action). It reports nil and
// shows an error when the re-dial fails.
func (ws *workspace) reconnect() db.Session {
	newConn := ws.conn
	newConn.Database = ws.currentDB
	sess, err := dialConn(newConn)
	if err != nil {
		showInfo(ws.window, "重连", "Reconnect failed:\n"+err.Error())
		return nil
	}
	if old := ws.liveSession(); old != nil {
		old.Close()
	}
	ws.setSession(sess)
	ws.conn = newConn
	ws.loadTLS()
	return sess
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
	ws.tabs.onCloseTab = func(i int) {
		if i == 0 {
			return // the data tab is never closeable
		}
		if i-1 < len(ws.queryTabs) {
			ws.queryTabs = append(ws.queryTabs[:i-1], ws.queryTabs[i:]...)
		}
		ws.tabs.removeTab(i)
		// Rebuild the content stack from the data stack + the surviving panes.
		ws.content.Objects = ws.content.Objects[:0]
		ws.content.Objects = append(ws.content.Objects, ws.dataStack)
		for _, qp := range ws.queryTabs {
			ws.content.Objects = append(ws.content.Objects, qp)
		}
		if ws.tabs.current >= len(ws.tabs.tabs) {
			ws.tabs.current = len(ws.tabs.tabs) - 1
		}
		ws.tabs.setActive(ws.tabs.current)
		ws.showTab(ws.tabs.current)
	}
	// seed tabs: one data tab (empty until a table is opened). SQL editor
	// tabs are only created on demand (SQL button / +), matching TablePlus.
	dataTab := ws.tabs.addTab("Data", false)
	dataTab.active = true
	ws.tabs.current = 0
	ws.tabs.tabs[0].setActive(true)

	// ---- sidebar (resizable, like TablePlus) ----
	ws.side = newSidebar(func(name string) {
		ws.openObjectByName(name)
	}, func() { ws.addQueryTab() })
	ws.side.onDBPick = func(name string) { ws.switchDatabase(name) }
	ws.side.onNewDB = func() { ws.newDatabase() }
	ws.side.onItemMenu = func(name string, pos fyne.Position) { ws.showItemMenu(name, pos) }
	ws.side.onQueryRun = func(q SavedQuery) { ws.openSavedQuery(q, true) }
	ws.side.onQueryOpen = func(q SavedQuery) { ws.openSavedQuery(q, false) }
	ws.side.onQuerySave = func() { ws.saveCurrentQuery() }
	ws.side.onQueryMenu = func(q SavedQuery, pos fyne.Position) { ws.showQueryMenu(q, pos) }
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

	// ---- crumb bar above the content area ----
	ws.crumbBar = ws.buildCrumbBar()

	// bottom: single TablePlus-style bar (segment + range text + paging).
	// Shown only on the data tab; the query tab has its own status bar and,
	// like TablePlus's query workspace, no 数据/结构/索引 segmented control.
	ws.bottomBar = container.NewVBox(ws.buildBottomBar())
	inner := container.NewBorder(ws.crumbBar, nil, nil, nil, ws.content)
	right := container.NewBorder(nil, ws.bottomBar, nil, nil, inner)

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
// Best-effort: unsupported drivers simply yield nothing.
func (ws *workspace) fetchFunctions() []TableInfo {
	if ws.sess == nil || ws.currentDB == "" {
		return nil
	}
	var q string
	switch ws.conn.Driver {
	case "postgres", "postgresql":
		q = "SELECT routine_name FROM information_schema.routines " +
			"WHERE routine_schema = current_schema() AND routine_type = 'FUNCTION' ORDER BY routine_name"
	case "mssql":
		q = "SELECT name FROM sys.objects WHERE type IN ('FN','IF','TF') ORDER BY name"
	default: // mysql / mariadb
		q = "SELECT routine_name FROM information_schema.routines " +
			"WHERE routine_schema = DATABASE() AND routine_type = 'FUNCTION' ORDER BY routine_name"
	}
	res, err := ws.sess.Query(q, 5000)
	if err != nil {
		return nil
	}
	var out []TableInfo
	for _, row := range res.Rows {
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
	safeGo("loadInventory", func() {
		objs, err := ws.sess.Tables()
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "Load Tables", "Could not list tables:\n"+err.Error())
				return
			}
			list := make([]TableInfo, 0, len(objs))
			for _, o := range objs {
				list = append(list, TableInfo{Name: o.Name, Kind: o.Kind, Rows: o.Rows, Approx: o.ApproxRows})
			}
			list = append(list, ws.fetchFunctions()...)
			ws.side.setInventory(list, ws.currentDB)
			if srv := ws.sess.Server(); srv != "" {
				ws.window.SetTitle("TablePro — " + ws.conn.Name + " — " + srv)
			}
			ws.refreshRibbon()
			// Like TablePlus: selecting a database only loads the tree — the
			// center stays blank until the user opens a table.
			ws.syncChrome()
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

// newDatabase is the sidebar "+" handler: creating a database is not supported
// in-app, so point the user at the SQL editor.
func (ws *workspace) newDatabase() {
	showInfo(ws.window, "新建数据库",
		"暂不支持在应用内创建数据库。\n请在 SQL 标签页执行 CREATE DATABASE。")
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
	p := val(res.Rows[0])
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

// buildStrip is the TablePlus top of the workspace:
//
//	row 1: toolbar — sidebar toggle | + | history | more | [db ▾] [SQL] |
//	      connection breadcrumb pill (center) | info, refresh, details toggle
//	row 2: document tabs (+ add button)
func (ws *workspace) buildStrip() fyne.CanvasObject {
	// ---- green server-info ribbon (click → pick another connection) ----
	// TablePlus format: "LOCAL | MySQL 5.7.40 : TLSv1.2 : host : db : table".
	ws.ribbon = newServerRibbon()
	ws.ribbon.onTap = func() { ws.openConnectionPicker() }
	ws.refreshRibbon()

	// ---- database selector pill (click → 打开数据库 sheet) ----
	dbName := ws.currentDB
	if dbName == "" {
		dbName = "Select Database"
	}
	ws.dbSelect = newCrumbPill(widget.NewIcon(theme.StorageIcon()), dbName,
		theme.MenuDropDownIcon(), func() { ws.openDatabasePicker() })

	// ---- left tools ----
	leftTools := container.NewHBox(
		newToolButton(theme.MenuIcon(), func() { ws.toggleSidebar() }),
		newToolButton(theme.ContentAddIcon(), func() { ws.addQueryTab() }),
		newToolButton(theme.HistoryIcon(), func() { ws.side.setMode(sidebarHistory) }),
		newToolButton(theme.MoreVerticalIcon(), func() { ws.openMoreMenu() }),
	)

	sqlBtn := newFlatButton("SQL")
	sqlBtn.onTap = func() { ws.addQueryTab() }

	// ---- right tools ----
	ws.editBtn = newToolButton(theme.DocumentCreateIcon(), func() { ws.toggleEditMode() })
	rightTools := container.NewHBox(
		newToolButton(theme.InfoIcon(), func() {
			showInfo(ws.window, "Connection", ws.connInfoText())
		}),
		ws.editBtn,
		newToolButton(theme.AccountIcon(), func() {
			showInfo(ws.window, "Authentication", "This connection has no additional authentication.")
		}),
		newToolButton(theme.LoginIcon(), func() { showLicenseDialog(ws.window) }),
		newToolButton(theme.ViewRefreshIcon(), func() { ws.reloadTable() }),
		newToolButton(theme.ListIcon(), func() { ws.toggleDetails() }),
	)

	toolRow := container.NewBorder(nil, nil,
		container.NewHBox(leftTools, spacerW(10), ws.dbSelect, spacerW(8), sqlBtn),
		container.NewPadded(rightTools),
		container.NewCenter(container.NewPadded(ws.ribbon)),
	)
	toolBox := newPanelBackground(container.NewPadded(toolRow),
		func() color.Color { return pal().stripBG })

	// ---- row 2: document tabs (full-width strip; the tab bar positions the
	// "+" button right after the last tab itself) ----
	tabBox := newPanelBackground(container.NewPadded(ws.tabs),
		func() color.Color { return pal().stripBG })

	return container.NewVBox(toolBox, tabBox)
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
		func() color.Color { return pal().contentBG })

	// ---- row 2: range text (left) + 筛选器/列 + paging (right) ----
	ws.prevBtn = newFlatButton("‹")
	ws.prevBtn.onTap = func() { ws.gotoOffset(ws.offset - browseLimit) }
	ws.nextBtn = newFlatButton("›")
	ws.nextBtn.onTap = func() { ws.gotoOffset(ws.offset + browseLimit) }
	pageMark := newFlatButton("≡")
	pageMark.onTap = func() {}
	filtersBtn := newFlatButton("Filter")
	filtersBtn.onTap = func() { ws.showFilterDialog() }
	columnsBtn := newFlatButton("Columns")
	columnsBtn.onTap = func() { ws.showColumnsDialog() }
	row2right := container.NewHBox(filtersBtn, spacerW(6), columnsBtn, spacerW(10),
		ws.prevBtn, pageMark, ws.nextBtn)
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
		func() color.Color { return pal().contentBG })

	return container.NewVBox(row1Box, row2)
}

// buildCrumbBar is the navigation row above the data area: < > arrows and
// the rounded current-table field.
func (ws *workspace) buildCrumbBar() fyne.CanvasObject {
	p := pal()
	prev := newToolButton(theme.NavigateBackIcon(), func() { ws.gotoOffset(ws.offset - browseLimit) })
	next := newToolButton(theme.NavigateNextIcon(), func() { ws.gotoOffset(ws.offset + browseLimit) })

	ws.crumbBox = newCrumbBox("")

	left := container.NewHBox(prev, next, spacerW(6))
	return container.NewBorder(nil, nil, left, nil,
		container.NewStack(
			canvas.NewRectangle(p.contentBG),
			container.NewPadded(ws.crumbBox),
		))
}

// flatButton: a light bordered button like Columns/Filters.
type flatButton struct {
	widget.BaseWidget
	label   string
	enabled bool
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

func (b *flatButton) Tapped(*fyne.PointEvent) {
	if b.enabled && b.onTap != nil {
		b.onTap()
	}
}
func (b *flatButton) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (b *flatButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().hover)
	bg.CornerRadius = 6
	bg.StrokeWidth = 1
	bg.StrokeColor = pal().line
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
	return fyne.NewSize(r.text.MinSize().Width+20, 26)
}

func (r *flatBtnRenderer) Destroy() {}
func (r *flatBtnRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	t := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-t.Width)/2, (size.Height-t.Height)/2))
}
func (r *flatBtnRenderer) Refresh() {
	p := pal()
	if !r.b.enabled {
		r.bg.FillColor = color.Transparent
		r.bg.StrokeColor = color.Transparent
		r.text.Color = p.gridNullFG
	} else {
		r.bg.FillColor = p.hover
		r.bg.StrokeColor = p.line
		r.text.Color = p.foreground
	}
	r.bg.Refresh()
	r.text.Refresh()
}

// tab management ------------------------------------------------------------

// newQueryTabPane builds one independent SQL tab: its own editor, result
// grid and find box. Each pane consults the workspace's live session, so
// switching databases never strands a captured (closed) connection.
func (ws *workspace) newQueryTabPane() *queryPane {
	qp := newQueryPane(ws.window, ws.conn, ws.sess, ws.side)
	qp.sessFn = func() db.Session { return ws.sess }
	qp.onReconnect = ws.reconnect
	qp.onRowSelect = ws.showQueryRow
	qp.Hide()
	return qp
}

// activeQueryPane returns the query pane for the current tab, or the last one
// if no tab is active. Used by the quick-open palette to fill an editor.
func (ws *workspace) activeQueryPane() *queryPane {
	if ws.tabs.current >= 1 && ws.tabs.current-1 < len(ws.queryTabs) {
		return ws.queryTabs[ws.tabs.current-1]
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
	ws.tabs.addTab(title, true)
	qp := ws.newQueryTabPane()
	ws.queryTabs = append(ws.queryTabs, qp)
	ws.content.Objects = append(ws.content.Objects, qp)
	idx := len(ws.queryTabs) // 1-based tab index (tab 0 is the data tab)
	ws.tabs.setActive(idx)
	ws.showTab(idx)
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
			if err := deleteSavedQuery(q.Name); err != nil {
				dialog.NewError(err, ws.window).Show()
				return
			}
			ws.side.refreshQueries()
		}),
	)
	widget.ShowPopUpMenuAtPosition(menu, canvas, pos)
}

// syncChrome shows/hides the data-view chrome (crumb bar + bottom bar). When
// no table is open on the data tab the center is left blank, like TablePlus
// right after a database is selected.
func (ws *workspace) syncChrome() {
	if ws.crumbBar == nil || ws.bottomBar == nil {
		return
	}
	dataTab := ws.tabs.current == 0
	hasTable := ws.tableName != ""
	if dataTab && !hasTable {
		ws.crumbBar.Hide()
		ws.bottomBar.Hide()
		return
	}
	ws.crumbBar.Show()
	if dataTab && hasTable {
		ws.bottomBar.Show() // data tab: 数据/结构/索引 + paging
	} else {
		ws.bottomBar.Hide() // query tab: editor + results only
	}
	if ws.body != nil {
		ws.body.Refresh()
	}
}

// showTab switches the visible content between the data stack (tab 0) and
// the matching independent query pane (every other tab).
func (ws *workspace) showTab(i int) {
	if i == 0 {
		ws.dataStack.Show()
	} else {
		ws.dataStack.Hide()
	}
	for k, qp := range ws.queryTabs {
		if k+1 == i {
			qp.Show()
		} else {
			qp.Hide()
		}
	}
	ws.syncChrome()
	ws.refreshRibbon()
	ws.content.Refresh()
}

// openTable loads the first page of a table (or view) into the data tab.
func (ws *workspace) openTable(name string) {
	if name != ws.tableName {
		ws.filterSQL = "" // filters never leak across tables
		ws.offset = 0
	}
	ws.tableName = name
	ws.updateBanner()
	if len(ws.tabs.tabs) > 0 {
		ws.tabs.tabs[0].setTitle(name)
	}
	ws.tabs.setActive(0)
	ws.showTab(0)
	ws.segment.set(0)

	if ws.crumbBox != nil {
		ws.crumbBox.setText(name)
	}
	ws.offset = 0
	ws.status.setFrom(1)
	ws.status.setTable(name)
	ws.status.setRows(0, 0, false)
	ws.updatePaging(false, false)
	// Swap in a fresh placeholder grid; the grid's renderer bakes in the
	// column layout, so data changes must replace the widget.
	ws.grid = newDataTable(emptyTableData(), ws.status)
	ws.prepGrid(ws.grid)
	ws.bindDataFind()
	ws.dataSlot.Objects = []fyne.CanvasObject{ws.grid}
	ws.dataSlot.Refresh()
	ws.tableCols = nil
	ws.refreshRowDetails()
	gen := ws.nextGen()
	safeGo("openTable "+name, func() {
		var res proto.QueryResult
		var err error
		if strings.TrimSpace(ws.filterSQL) == "" {
			res, err = ws.sess.Browse(name, browseLimit)
		} else {
			res, err = ws.sess.Query(
				fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT %d",
					ws.quoteTable(name), ws.filterSQL, browseLimit), browseLimit)
		}
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "Load "+name, err.Error())
				return
			}
			data := tableDataFromResult(res, -1, false)
			ws.totalRows = data.Fetched
			ws.totalApprox = false
			ws.grid = newDataTable(data, ws.status)
			ws.prepGrid(ws.grid)
			ws.bindDataFind()
			ws.dataSlot.Objects = []fyne.CanvasObject{ws.grid}
			ws.dataSlot.Refresh()
			ws.status.setColumns(len(data.Cols))
			ws.status.setRows(data.Fetched, data.Total, false)
			ws.updatePaging(false, len(data.Rows) >= browseLimit)

			// structure pane + approximate total in the background
			ws.loadStructure(name, gen)
			safeGo("approxRows "+name, func() {
				total, approx := ws.approxRows(name)
				safeDo(func() {
					if !ws.current(gen) || ws.sess == nil {
						return
					}
					ws.totalRows = total
					ws.totalApprox = approx
					ws.status.setRows(data.Fetched, total, approx)
				})
			})
			// right details sidebar shows the selected row's fields
			ws.refreshRowDetails()
			safeGo("indexes "+name, func() {
				idx := fetchIndexes(ws.sess, ws.conn.Database, name)
				safeDo(func() {
					if !ws.current(gen) || ws.sess == nil {
						return
					}
					if idx != nil {
						ws.indexPane.Objects = []fyne.CanvasObject{newListPane(
							[]string{"Name", "Column", "Unique", "Type"}, indexRows(idx))}
						ws.indexPane.Hide()
						ws.indexPane.Refresh()
					}
				})
			})
		})
	})
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
	case "mysql", "mariadb", "postgres", "postgresql":
		return true
	}
	return false
}

func (ws *workspace) quoteTable(name string) string {
	switch ws.conn.Driver {
	case "mysql", "mariadb":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
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
}

// gotoOffset loads one page of the current table starting at row off.
func (ws *workspace) gotoOffset(off int) {
	if ws.tableName == "" || ws.sess == nil {
		return
	}
	if !ws.pageSupported() && ws.filterSQL == "" {
		return
	}
	if off < 0 {
		off = 0
	}
	if off == ws.offset {
		return
	}
	name := ws.tableName
	gen := ws.nextGen()
	safeGo("page "+name, func() {
		sql := fmt.Sprintf("SELECT * FROM %s%s LIMIT %d OFFSET %d",
			ws.quoteTable(name), ws.whereClause(), browseLimit+1, off)
		res, err := ws.sess.Query(sql, browseLimit+1)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "翻页", err.Error())
				return
			}
			hasMore := len(res.Rows) > browseLimit
			if hasMore {
				res.Rows = res.Rows[:browseLimit]
			}
			data := tableDataFromResult(res, ws.totalRows, ws.totalApprox)
			ws.offset = off
			ws.grid = newDataTable(data, ws.status)
			ws.prepGrid(ws.grid)
			ws.bindDataFind()
			ws.dataSlot.Objects = []fyne.CanvasObject{ws.grid}
			ws.dataSlot.Refresh()
			ws.status.setFrom(off + 1)
			ws.status.setRows(len(data.Rows), data.Total, data.Approx)
			ws.updatePaging(off > 0, hasMore)
		})
	})
}

// loadIndexes refreshes the index pane for the open table.
func (ws *workspace) loadIndexes() {
	if ws.tableName == "" {
		return
	}
	name := ws.tableName
	gen := ws.nextGen()
	safeGo("indexes-load "+name, func() {
		idx := fetchIndexes(ws.sess, ws.conn.Database, name)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if idx != nil {
				ws.indexPane.Objects = []fyne.CanvasObject{newListPane(
					[]string{"Name", "Column", "Unique", "Type"}, indexRows(idx))}
				ws.indexPane.Refresh()
			}
		})
	})
}

// approxRows looks up the inventory entry for the current table.
func (ws *workspace) approxRows(name string) (int, bool) {
	objs, err := ws.sess.Tables()
	if err != nil {
		return -1, false
	}
	for _, o := range objs {
		if o.Name == name {
			return int(o.Rows), o.ApproxRows && o.Rows >= 0
		}
	}
	return -1, false
}

// loadStructure fetches real column metadata for the Structure pane, in the
// background (must not block the UI thread).
func (ws *workspace) loadStructure(table string, gen int) {
	safeGo("loadStructure "+table, func() {
		cols, err := ws.sess.Columns(table)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			data := &TableData{}
			if err == nil {
				for _, c := range cols {
					data.Cols = append(data.Cols, ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Key: c.Key})
				}
			}
			ws.structPane.Objects = []fyne.CanvasObject{newStructurePane(data)}
			ws.structPane.Hide()
			ws.structPane.Refresh()
			if err == nil {
				// The right 详情 panel shows the table structure until a row
				// is selected, so the table's columns are always visible.
				ws.tableCols = data.Cols
				ws.refreshRowDetails()
			}
			if err != nil {
				showInfo(ws.window, "Structure "+table, err.Error())
			}
		})
	})
}

func (ws *workspace) reloadTable() {
	if ws.tableName == "" {
		ws.loadInventory()
		return
	}
	ws.openTable(ws.tableName)
}
