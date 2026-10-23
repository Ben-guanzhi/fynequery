package main

// Connection model + persistence (connections.json under the user config
// dir) and the display metadata (badges, tag colors) that goes with it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fynequery/db"
)

// Connection is one saved connection entry. Passwords default to plain text
// in this file (local developer tool); when UseKeyring is set the password
// lives in the OS keyring instead (Windows Credential Manager) and the JSON
// copy is left empty.
type Connection struct {
	Name        string       `json:"name"`
	Driver      string       `json:"driver"` // driver code: mysql, postgres, mssql, oracle...
	Host        string       `json:"host"`
	Port        string       `json:"port"`
	User        string       `json:"user"`
	Password    string       `json:"password"`
	Database    string       `json:"database"`
	Group       string       `json:"group"`
	Tags        []string     `json:"tags,omitempty"`
	StatusColor string       `json:"statusColor,omitempty"` // preset name (TablePlus Status Color)
	SafeMode    int          `json:"safeMode"`              // 0-4, see safe_gate.go (0 = silent)
	UseKeyring  bool         `json:"keyring,omitempty"`     // store the password in the OS keyring
	SSH         db.SSHConfig `json:"ssh,omitempty"`         // optional SSH bastion tunnel
}

// resolvePassword fills Password from the OS keyring when the JSON copy is
// empty (keyring-stored connections).
func (c *Connection) resolvePassword() {
	if c.Password != "" || !c.UseKeyring {
		return
	}
	if v, ok := keyringGet(c.Name); ok {
		c.Password = v
	}
}

// Params converts the saved connection into driver dial parameters.
func (c *Connection) Params() db.ConnectParams {
	return db.ConnectParams{
		Name:     c.Name,
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		Database: c.Database,
	}
}

// fingerprint identifies a connection for the session pool: same engine,
// host/port/user/database, credentials, and SSH settings map to the same
// cached session. Two distinct connections (or a changed password) never share
// a session.
func (c Connection) fingerprint() string {
	s := c.SSH
	return strings.Join([]string{
		"v1",
		c.Driver,
		c.Host,
		c.Port,
		c.User,
		c.Database,
		c.Password,
		"ssh",
		strconv.FormatBool(s.Enabled),
		s.Host,
		s.Port,
		s.User,
		s.AuthMode,
	}, "|")
}

// dialConn opens a live session for a saved connection using the driver
// registry (built-in MySQL or a discovered plugin). When the connection
// enables an SSH tunnel, the session is opened through a host-side local
// forward so the database driver connects to 127.0.0.1:<localPort>.
//
// Sessions are returned through the global session pool, so re-opening a
// recently used connection reuses the underlying (plugin/SSH) session instead
// of reconnecting; Close() on the returned session releases it back to the
// pool rather than disconnecting.
func dialConn(c Connection) (db.Session, error) {
	c.resolvePassword()
	d, ok := db.Driver(c.Driver)
	if !ok {
		return nil, fmt.Errorf("no driver registered for %q (plugin missing?)", c.Driver)
	}
	cp := c.Params()
	fp := c.fingerprint()
	open := func() (db.Session, error) {
		if c.SSH.Enabled {
			t, err := db.OpenTunnel(c.SSH, c.Host, c.Port)
			if err != nil {
				return nil, err
			}
			cp.Host = "127.0.0.1"
			cp.Port = strconv.Itoa(t.LocalPort())
			sess, err := d.Connect(cp)
			if err != nil {
				t.Close()
				return nil, err
			}
			return db.NewTunneledSession(sess, t), nil
		}
		return d.Connect(cp)
	}
	return db.DefaultPool.Get(fp, open)
}

func configDir() string {
	// Test/portable override: point the whole config (connections.json, keyring
	// salt, logs) at a scratch dir. Unset in normal runs → the user config dir.
	if d := os.Getenv("FYNEQUERY_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "fynequery")
}

func connectionsPath() string { return filepath.Join(configDir(), "connections.json") }

// loadConnections reads the saved connections; a missing file yields an
// empty list, a corrupt file is reported but not fatal.
func loadConnections() ([]Connection, error) {
	raw, err := os.ReadFile(connectionsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var conns []Connection
	if err := json.Unmarshal(raw, &conns); err != nil {
		return nil, fmt.Errorf("parse %s: %w", connectionsPath(), err)
	}
	return conns, nil
}

func saveConnections(conns []Connection) error {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(conns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(connectionsPath(), raw, 0o600)
}

// upsertConnection inserts or updates by name and persists. When UseKeyring
// is set and a password is present, the password is moved to the OS keyring
// and the JSON copy is emptied.
func upsertConnection(c Connection) error {
	if c.UseKeyring && keyringAvailable() && c.Password != "" {
		if err := keyringSet(c.Name, c.Password); err != nil {
			return fmt.Errorf("keyring: %w", err)
		}
		c.Password = "" // JSON keeps only the flag, not the secret
	} else if c.UseKeyring && !keyringAvailable() {
		// A connection imported from a platform with a keyring set the flag, but
		// here there is no keyring: fall back to plaintext storage and clear the
		// flag so we never claim the secret is protected when it is not.
		c.UseKeyring = false
	}
	conns, err := loadConnections()
	if err != nil {
		conns = nil
	}
	replaced := false
	for i := range conns {
		if conns[i].Name == c.Name {
			conns[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		conns = append(conns, c)
	}
	return saveConnections(conns)
}

func deleteConnection(name string) error {
	conns, err := loadConnections()
	if err != nil {
		return err
	}
	out := conns[:0]
	for _, c := range conns {
		if c.Name != name {
			out = append(out, c)
		}
	}
	if err := saveConnections(out); err != nil {
		return err
	}
	_ = keyringDelete(name) // best effort: orphaned creds are harmless
	return nil
}

// ---- display metadata ------------------------------------------------------

// DBType carries the badge look for a driver.
type DBType struct {
	Name   string
	Code   string
	Color  string
	Light  bool
	Driver string
}

var dbTypeStyles = map[string]struct {
	Code  string
	Color string
	Light bool
}{
	"mysql":    {Code: "My", Color: "#F5A623"},
	"postgres": {Code: "Pg", Color: "#1B2A4A"},
	"mssql":    {Code: "MS", Color: "#5BC0DE", Light: true},
	"oracle":   {Code: "Or", Color: "#C74634"},
	"sqlite":   {Code: "SL", Color: "#0F80CC", Light: true},
}

var fallbackColors = []string{"#2F7BD9", "#3E8E8E", "#8E44AD", "#E8862E", "#16A085", "#B03A2E"}

// dbTypeFor builds badge metadata from a registered driver.
func dbTypeFor(info db.DriverInfo) DBType {
	if s, ok := dbTypeStyles[info.Code]; ok {
		return DBType{Name: info.Name, Code: s.Code, Color: s.Color, Light: s.Light, Driver: info.Code}
	}
	code := []rune(info.Name)
	if len(code) >= 2 {
		code = code[:2]
	}
	color := fallbackColors[0]
	if len(info.Code) > 0 {
		sum := 0
		for _, r := range info.Code {
			sum += int(r)
		}
		color = fallbackColors[sum%len(fallbackColors)]
	}
	return DBType{Name: info.Name, Code: string(code), Color: color, Driver: info.Code}
}

func dbTypeByDriver(driverCode string) DBType {
	if d, ok := db.Driver(driverCode); ok {
		return dbTypeFor(d.Info())
	}
	return DBType{Name: driverCode, Code: "?", Color: "#9E9E9E", Light: true, Driver: driverCode}
}

// statusColorPresets are the connection status colours (TablePlus "Status
// Color"): a dot/ribbon accent that separates production from sandbox.
var statusColorPresets = map[string]string{
	"None":   "",
	"Green":  "#4CAF50",
	"Orange": "#FF9800",
	"Red":    "#E53935",
	"Blue":   "#2F7BD9",
	"Purple": "#9C27B0",
	"Grey":   "#9E9E9E",
}

// statusColorNames is the ordered preset list for the connection form.
var statusColorNames = []string{"None", "Green", "Orange", "Red", "Blue", "Purple", "Grey"}

// StatusColorHex resolves the connection's preset to a hex colour, "" for
// "None"/unset (and unknown names, e.g. old custom presets).
func (c *Connection) StatusColorHex() string {
	if h, ok := statusColorPresets[c.StatusColor]; ok {
		return h
	}
	return ""
}

// tagColor picks a stable chip color for a tag label.
func tagColor(label string) string {
	if label == "loc" {
		return "#4CAF50"
	}
	if label == "tes" {
		return "#9C27B0"
	}
	sum := 0
	for _, r := range label {
		sum += int(r)
	}
	return fallbackColors[sum%len(fallbackColors)]
}

// Tag is a small colored chip shown next to a connection name.
type Tag struct {
	Label string
	Color string
}

func tagsFor(c Connection) []Tag {
	out := make([]Tag, 0, len(c.Tags))
	for _, l := range c.Tags {
		out = append(out, Tag{Label: l, Color: tagColor(l)})
	}
	return out
}

// connHostLine renders the secondary line under a connection name (redacted
// in privacy mode so shared screens never show where the server lives).
func connHostLine(c Connection) string {
	t := dbTypeByDriver(c.Driver)
	if c.Host == "" {
		return fmt.Sprintf("%s · %s", t.Name, maskedPart(c.Database))
	}
	return fmt.Sprintf("%s · %s:%s · %s",
		t.Name, maskedPart(c.Host), maskedPart(c.Port), maskedPart(c.Database))
}

// ConnGroup is a group of connections in the welcome list.
type ConnGroup struct {
	Name  string
	Color string
	Conns []Connection
}

var groupColors = []string{"#2F7BD9", "#E8862E", "#3E8E8E", "#8E44AD", "#16A085"}

// groupConnections buckets saved connections by their group field, keeping
// insertion order.
func groupConnections(conns []Connection) []ConnGroup {
	var order []string
	buckets := map[string][]Connection{}
	for _, c := range conns {
		g := c.Group
		if g == "" {
			g = "Others"
		}
		if _, seen := buckets[g]; !seen {
			order = append(order, g)
		}
		buckets[g] = append(buckets[g], c)
	}
	out := make([]ConnGroup, 0, len(order))
	for i, g := range order {
		out = append(out, ConnGroup{
			Name:  g,
			Color: groupColors[i%len(groupColors)],
			Conns: buckets[g],
		})
	}
	return out
}
