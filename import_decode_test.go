package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// decodeImportBytes 端到端用例: 表ProGo 的 DecodingReader 同款算法（移植 P3-7），
// 覆盖 BOM 三态 + GB18030 回落 + UTF-8 透传 + 截断 rune 容忍。
func TestDecodeImportBytes(t *testing.T) {
	// GB18030: "姓名,城市" 编码后不再是合法 UTF-8，必须被解码回来。
	gbk, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("姓名,城市\n张三,北京"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if utf8.Valid(gbk) {
		t.Fatal("test fixture broken: GB18030 bytes unexpectedly valid UTF-8")
	}
	got := decodeImportBytes(gbk)
	if !utf8.Valid(got) || !strings.Contains(string(got), "姓名,城市") {
		t.Fatalf("GB18030 decode = %q, want 姓名,城市…", got)
	}

	// UTF-16LE 带 BOM。
	le, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder(), []byte("\ufeffcol\r\nval"))
	if err != nil {
		t.Fatalf("utf16 encode: %v", err)
	}
	got = decodeImportBytes(le)
	if !strings.Contains(string(got), "col") || !strings.Contains(string(got), "val") {
		t.Fatalf("UTF-16LE decode = %q", got)
	}

	// UTF-8 带 BOM: 剥离 BOM，其余透传。
	if got := decodeImportBytes([]byte("\xef\xbb\xbfhello")); string(got) != "hello" {
		t.Fatalf("UTF-8 BOM decode = %q, want hello", got)
	}

	// 纯 UTF-8: 透传不改。
	src := []byte("name,city\nzhang,beijing")
	if got := decodeImportBytes(src); string(got) != string(src) {
		t.Fatalf("plain UTF-8 must pass through, got %q", got)
	}

	// 末尾截断的多字节 rune: 容忍（ genuine UTF-8 被切尾 3 字节内）。
	truncated := []byte("name\n张三")[:len("name\n张三")-1] // 掐掉最后一个字节
	if got := decodeImportBytes(truncated); string(got) != "name\n张" {
		t.Fatalf("truncated rune tolerance = %q, want name\\n张", got)
	}

	// UTF-16BE 带 BOM(BE 分支此前零覆盖)。
	be, _, err := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder(), []byte("\ufeffcol\r\nval"))
	if err != nil {
		t.Fatalf("utf16be encode: %v", err)
	}
	got = decodeImportBytes(be)
	if !strings.Contains(string(got), "col") || !strings.Contains(string(got), "val") {
		t.Fatalf("UTF-16BE BOM decode = %q", got)
	}

	// 无 BOM 的 UTF-16: NUL 密度嗅探分清 LE/BE(ASCII 文本每隔一字节一个 NUL)。
	leNoBOM, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder(), []byte("name,value\n1,2"))
	if err != nil {
		t.Fatalf("utf16le-noBOM encode: %v", err)
	}
	if got := decodeImportBytes(leNoBOM); !strings.Contains(string(got), "name,value") {
		t.Fatalf("UTF-16LE no-BOM sniff decode = %q", got)
	}
	beNoBOM, _, err := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewEncoder(), []byte("name,value\n1,2"))
	if err != nil {
		t.Fatalf("utf16be-noBOM encode: %v", err)
	}
	if got := decodeImportBytes(beNoBOM); !strings.Contains(string(got), "name,value") {
		t.Fatalf("UTF-16BE no-BOM sniff decode = %q", got)
	}

	// 极短文件(< 8 字节): 嗅探放弃,按 UTF-8 透传,不得越界。
	if got := decodeImportBytes([]byte("a,b")); string(got) != "a,b" {
		t.Fatalf("tiny file passthrough = %q", got)
	}
	if got := decodeImportBytes(nil); len(got) != 0 {
		t.Fatalf("empty input = %q", got)
	}
}

// 端到端: GB18030 编码的 CSV 经 parseImport 解析后表头与单元格都是正确文本。
func TestParseImportDecodesGB18030(t *testing.T) {
	src, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("姓名,城市\n张三,北京"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rows, headers, err := parseImport(src, "customers.csv")
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}
	if len(headers) != 2 || headers[0] != "姓名" || headers[1] != "城市" {
		t.Fatalf("headers = %v, want [姓名 城市]", headers)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["姓名"] == nil || *rows[0]["姓名"] != "张三" {
		t.Fatalf("row 姓名 = %v", rows[0]["姓名"])
	}
	if rows[0]["城市"] == nil || *rows[0]["城市"] != "北京" {
		t.Fatalf("row 城市 = %v", rows[0]["城市"])
	}
}
