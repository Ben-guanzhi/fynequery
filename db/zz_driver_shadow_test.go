package db

// 内置驱动不可被插件遮蔽：一个恰同名的插件（fq-plugin-mysql.exe → code
// "mysql"）覆盖编译进二进制的驱动 = 用户实际用的驱动悄悄换成外部进程。

import (
	"testing"
)

type stubDialer struct {
	info DriverInfo
}

func (s stubDialer) Info() DriverInfo { return s.info }
func (s stubDialer) Connect(p ConnectParams) (Session, error) {
	return nil, nil
}

func TestBuiltinDriverCannotBeShadowedByPlugin(t *testing.T) {
	builtin := stubDialer{info: DriverInfo{Code: "x", Name: "Built-in X", Version: "builtin"}}
	plugin := stubDialer{info: DriverInfo{Code: "x", Name: "Plugin X", Version: "1.0", Plugin: true}}

	Register(builtin)
	Register(plugin)
	got, ok := Driver("x")
	if !ok {
		t.Fatal("驱动 x 未注册")
	}
	info := got.Info()
	if info.Plugin {
		t.Fatalf("内置驱动被插件遮蔽了: %+v", info)
	}
	if info.Version != "builtin" {
		t.Fatalf("注册的是 %q，内置被覆盖", info.Version)
	}
}

func TestPluginCanReplacePluginOfSameCode(t *testing.T) {
	p1 := stubDialer{info: DriverInfo{Code: "y", Name: "Plugin Y", Version: "1.0", Plugin: true}}
	p2 := stubDialer{info: DriverInfo{Code: "y", Name: "Plugin Y", Version: "2.0", Plugin: true}}

	if !Register(p1) {
		t.Fatal("插件首次注册应成功")
	}
	if !Register(p2) {
		t.Fatal("插件替换插件应成功（热更新路径）")
	}
	got, ok := Driver("y")
	if !ok {
		t.Fatal("驱动 y 未注册")
	}
	if got.Info().Version != "2.0" {
		t.Fatalf("插件未更新: %+v", got.Info())
	}
}
