package proto

import (
	"testing"

	"github.com/dbcore/dbcore/plugindriver"
)

// The protocol moved into the kernel, so the two clients' hosts must accept each
// other's plugins. These tests pin that: they are what stops a "one protocol now"
// claim from quietly becoming "two protocols that look alike".

func TestBannerNamesBothVocabularies(t *testing.T) {
	banner := KernelBanner("postgres", "PostgreSQL", "1.0")
	if banner.Code != "postgres" {
		t.Fatalf("the fynequery field is %q", banner.Code)
	}
	if banner.Kernel != "postgres" {
		t.Fatalf("the kernel field is %q", banner.Kernel)
	}
	if banner.Proto != plugindriver.ProtocolName {
		t.Fatalf("the protocol name is %q, want %q", banner.Proto, plugindriver.ProtocolName)
	}
}

// A kernel host reads "kind"; a fynequery host reads "code". A plugin banner must
// satisfy both.
func TestBannerSatisfiesBothHosts(t *testing.T) {
	banner := KernelBanner("mysql", "MySQL", "1.0")

	// The kernel host's check: the banner's kernel field names the engine.
	kernelBanner := plugindriver.Banner{
		Proto:   banner.Proto,
		Version: banner.Version,
		Kind:    banner.Kernel,
		Name:    banner.Name,
	}
	if kernelBanner.Kind != "mysql" {
		t.Fatalf("the kernel host would read %q", kernelBanner.Kind)
	}

	// The fynequery host's check: the legacy name and the fynequery field.
	if err := AcceptsBanner(banner); err != nil {
		t.Fatalf("the fynequery host refuses its own plugin: %v", err)
	}
}

// The plugins already deployed announce the legacy name; the host must still take
// them, or the move would orphan every one of them.
func TestLegacyBannerIsStillAccepted(t *testing.T) {
	legacy := Banner{Proto: LegacyProtocolName, Version: ProtocolVersion, Code: "oracle", Name: "Oracle"}
	if err := AcceptsBanner(legacy); err != nil {
		t.Fatalf("a deployed plugin was refused: %v", err)
	}
	// An unknown protocol is still refused.
	if err := AcceptsBanner(Banner{Proto: "something-else", Version: 1}); err == nil {
		t.Fatal("an unknown protocol must be refused")
	}
}

// The envelopes are the kernel's, so a frame written by one is read by the other.
func TestEnvelopesAreTheKernels(t *testing.T) {
	var request Request = plugindriver.Request{ID: 7, Method: MethodQuery}
	if request.ID != 7 || request.Method != MethodQuery {
		t.Fatalf("request = %+v", request)
	}
	var response Response = plugindriver.Response{ID: 7, Error: "boom"}
	if response.Error != "boom" {
		t.Fatalf("response = %+v", response)
	}
}

// The method names the deployed plugins answer to are unchanged; the kernel's
// equivalent names are listed beside them.
func TestMethodNamesMatchTheKernel(t *testing.T) {
	pairs := map[string]string{
		MethodMeta:        plugindriver.MethodOpenMeta,
		MethodOpen:        plugindriver.MethodOpen,
		MethodTables:      plugindriver.MethodListConnTables,
		MethodColumns:     plugindriver.MethodListConnColumns,
		MethodQuery:       plugindriver.MethodConnQuery,
		MethodBrowse:      plugindriver.MethodConnBrowse,
		MethodCreateDB:    plugindriver.MethodConnCreateDB,
		MethodUpdate:      plugindriver.MethodConnUpdate,
		MethodInsert:      plugindriver.MethodConnInsert,
		MethodDelete:      plugindriver.MethodConnDelete,
		MethodApplyBatch:  plugindriver.MethodConnApplyBatch,
		MethodBegin:       plugindriver.MethodConnBegin,
		MethodCommit:      plugindriver.MethodConnCommit,
		MethodRollback:    plugindriver.MethodConnRollback,
		MethodQueryStream: plugindriver.MethodConnQueryStream,
		MethodMetrics:     plugindriver.MethodConnMetrics,
	}
	for local, kernel := range pairs {
		if KernelMethodFor(local) != kernel {
			t.Errorf("%s maps to %q, want %q", local, KernelMethodFor(local), kernel)
		}
	}
}

// KernelMethodFor returns the kernel's name for a fynequery method.
func KernelMethodFor(local string) string {
	switch local {
	case MethodMeta:
		return KernelMethodMeta
	case MethodOpen:
		return KernelMethodOpen
	case MethodClose:
		return KernelMethodClose
	case MethodTables:
		return KernelMethodTables
	case MethodColumns:
		return KernelMethodColumns
	case MethodQuery:
		return KernelMethodQuery
	case MethodBrowse:
		return KernelMethodBrowse
	case MethodCreateDB:
		return KernelMethodCreateDB
	case MethodUpdate:
		return KernelMethodUpdate
	case MethodInsert:
		return KernelMethodInsert
	case MethodDelete:
		return KernelMethodDelete
	case MethodApplyBatch:
		return KernelMethodApplyBatch
	case MethodBegin:
		return KernelMethodBegin
	case MethodCommit:
		return KernelMethodCommit
	case MethodRollback:
		return KernelMethodRollback
	case MethodQueryStream:
		return KernelMethodQueryStream
	case MethodMetrics:
		return KernelMethodMetrics
	}
	return ""
}

// The cell conversions keep a NULL distinct from the empty string, which is the
// one thing the string form must not lose.
func TestCellConversionsKeepNullDistinct(t *testing.T) {
	row := []*string{nil, strp(""), strp("x")}
	cells := StringsToCells(row)
	if cells[0].Kind != "null" {
		t.Fatalf("a nil cell became %q", cells[0].Kind)
	}
	if cells[1].Kind != "text" || cells[1].Text != "" {
		t.Fatalf("an empty string became %+v", cells[1])
	}
	back := CellsToStrings(cells)
	if back[0] != nil {
		t.Fatal("NULL came back as a value")
	}
	if back[1] == nil || *back[1] != "" {
		t.Fatalf("the empty string came back as %v", back[1])
	}
	if back[2] == nil || *back[2] != "x" {
		t.Fatalf("the text came back as %v", back[2])
	}
}

func strp(s string) *string { return &s }
