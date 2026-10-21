package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// pluginsSubdir is where driver-plugin binaries live, under the state dir or
// beside the executable.
const pluginsSubdir = "plugins"

// pluginsDirs lists the folders scanned for installed driver plugins, in
// priority order: beside the executable first (so a portable build finds the
// plugins shipped next to it), then under the state dir.
func pluginsDirs() []string {
	var dirs []string
	if ex := exeDirPath(); ex != "" {
		dirs = append(dirs, filepath.Join(ex, pluginsSubdir))
	}
	dirs = append(dirs, filepath.Join(stateDir(), pluginsSubdir))
	return dirs
}

// PluginsDir is the primary plugins folder (under the state dir), named in
// messages that tell the user where to install a driver.
func PluginsDir() string {
	return filepath.Join(stateDir(), pluginsSubdir)
}

// pluginFileName is the on-disk name of a plugin for the current OS: the engine
// identifier, with .exe appended on Windows.
func pluginFileName(dbType string) string {
	if runtime.GOOS == "windows" {
		return dbType + ".exe"
	}
	return dbType
}

// PluginBinary resolves the executable for an engine's driver plugin, or explains
// that it is not installed. This is the "install the plugin to use this database"
// seam: an engine with no plugin binary cannot be connected to.
func PluginBinary(dbType string) (string, error) {
	name := pluginFileName(dbType)
	for _, dir := range pluginsDirs() {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("未安装 %s 驱动插件：把可执行文件 %s 放到 %s（或可执行文件旁的 %s 目录）后重试，可用 task build-plugins 生成",
		dbType, name, PluginsDir(), pluginsSubdir)
}

// InstalledDrivers reports which engine driver plugins are present, so the
// connection dialog can mark an engine as available or as needing a plugin.
func InstalledDrivers() map[string]bool {
	out := map[string]bool{}
	for _, dir := range pluginsDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if runtime.GOOS == "windows" {
				name = trimExe(name)
			}
			out[name] = true
		}
	}
	return out
}

func trimExe(name string) string {
	if ext := filepath.Ext(name); ext == ".exe" {
		return name[:len(name)-len(ext)]
	}
	return name
}

// BuiltinDrivers are the engines that run in-process and need no plugin.
func BuiltinDrivers() map[string]bool {
	return map[string]bool{
		DbTypeMySQL:  true,
		DbTypeSQLite: true,
	}
}
