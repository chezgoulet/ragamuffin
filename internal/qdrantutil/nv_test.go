package qdrantutil

import (
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── Nv ───────────────────────────────────────────────────────────────────────

func TestNv_String(t *testing.T) {
	v := Nv("hello")
	if v.GetStringValue() != "hello" {
		t.Errorf("expected 'hello', got %v", v)
	}
}

func TestNv_Bool(t *testing.T) {
	v := Nv(true)
	if !v.GetBoolValue() {
		t.Error("expected true")
	}
}

func TestNv_Float(t *testing.T) {
	v := Nv(3.14)
	if v.GetDoubleValue() != 3.14 {
		t.Errorf("expected 3.14, got %f", v.GetDoubleValue())
	}
}

func TestNv_Int(t *testing.T) {
	v := Nv(float64(42))
	if v.GetDoubleValue() != 42 {
		t.Errorf("expected 42, got %f", v.GetDoubleValue())
	}
}

func TestNv_ZeroString(t *testing.T) {
	v := Nv("")
	if v.GetStringValue() != "" {
		t.Error("expected empty string")
	}
}

// ── NvList ───────────────────────────────────────────────────────────────────

func TestNvList_Empty(t *testing.T) {
	v := NvList(nil)
	if v == nil {
		t.Fatal("expected non-nil value")
	}
	items := v.GetListValue().GetValues()
	if items == nil {
		t.Fatal("expected non-nil list")
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestNvList_Strings(t *testing.T) {
	items := []string{"a", "b", "c"}
	v := NvList(items)
	list := v.GetListValue().GetValues()
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}
	for i, s := range items {
		if list[i].GetStringValue() != s {
			t.Errorf("expected %q at %d, got %q", s, i, list[i].GetStringValue())
		}
	}
}

// ── GetPayloadString ─────────────────────────────────────────────────────────

func TestGetPayloadString_Found(t *testing.T) {
	payload := map[string]*pb.Value{"name": Nv("alice")}
	v, ok := GetPayloadString(payload, "name")
	if !ok {
		t.Fatal("expected ok")
	}
	if v != "alice" {
		t.Errorf("expected 'alice', got %q", v)
	}
}

func TestGetPayloadString_Missing(t *testing.T) {
	payload := map[string]*pb.Value{}
	_, ok := GetPayloadString(payload, "missing")
	if ok {
		t.Fatal("expected not ok")
	}
}

func TestGetPayloadString_NilValue(t *testing.T) {
	payload := map[string]*pb.Value{"key": nil}
	_, ok := GetPayloadString(payload, "key")
	if ok {
		t.Fatal("expected not ok for nil value")
	}
}

func TestGetPayloadString_NilPayload(t *testing.T) {
	_, ok := GetPayloadString(nil, "key")
	if ok {
		t.Fatal("expected not ok for nil payload")
	}
}

// ── GetPayloadStringList ─────────────────────────────────────────────────────

func TestGetPayloadStringList_Missing(t *testing.T) {
	result := GetPayloadStringList(map[string]*pb.Value{}, "missing")
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestGetPayloadStringList_StringValue(t *testing.T) {
	payload := map[string]*pb.Value{"tags": Nv("single")}
	result := GetPayloadStringList(payload, "tags")
	if len(result) != 1 || result[0] != "single" {
		t.Errorf("expected ['single'], got %v", result)
	}
}

func TestGetPayloadStringList_ListValue(t *testing.T) {
	payload := map[string]*pb.Value{"tags": NvList([]string{"a", "b", "c"})}
	result := GetPayloadStringList(payload, "tags")
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("expected ['a','b','c'], got %v", result)
	}
}

func TestGetPayloadStringList_NilPayload(t *testing.T) {
	if r := GetPayloadStringList(nil, "key"); r != nil {
		t.Errorf("expected nil, got %v", r)
	}
}

// ── GetPayloadFloat ──────────────────────────────────────────────────────────

func TestGetPayloadFloat_Found(t *testing.T) {
	payload := map[string]*pb.Value{"score": Nv(0.95)}
	v, ok := GetPayloadFloat(payload, "score")
	if !ok {
		t.Fatal("expected ok")
	}
	if v != 0.95 {
		t.Errorf("expected 0.95, got %f", v)
	}
}

func TestGetPayloadFloat_Missing(t *testing.T) {
	_, ok := GetPayloadFloat(map[string]*pb.Value{}, "missing")
	if ok {
		t.Fatal("expected not ok")
	}
}

func TestGetPayloadFloat_NilPayload(t *testing.T) {
	_, ok := GetPayloadFloat(nil, "key")
	if ok {
		t.Fatal("expected not ok")
	}
}

// ── GetPayloadBool ───────────────────────────────────────────────────────────

func TestGetPayloadBool_True(t *testing.T) {
	payload := map[string]*pb.Value{"flag": Nv(true)}
	v, ok := GetPayloadBool(payload, "flag")
	if !ok || !v {
		t.Error("expected true, true")
	}
}

func TestGetPayloadBool_False(t *testing.T) {
	payload := map[string]*pb.Value{"flag": Nv(false)}
	v, ok := GetPayloadBool(payload, "flag")
	if !ok || v {
		t.Error("expected true, false")
	}
}

func TestGetPayloadBool_Missing(t *testing.T) {
	_, ok := GetPayloadBool(map[string]*pb.Value{}, "missing")
	if ok {
		t.Fatal("expected not ok")
	}
}

// ── GetPayloadInt ────────────────────────────────────────────────────────────

func TestGetPayloadInt_Found(t *testing.T) {
	payload := map[string]*pb.Value{"count": Nv(float64(42))}
	v, ok := GetPayloadInt(payload, "count")
	if !ok || v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
}

func TestGetPayloadInt_Missing(t *testing.T) {
	_, ok := GetPayloadInt(map[string]*pb.Value{}, "missing")
	if ok {
		t.Fatal("expected not ok")
	}
}

// ── GetPayload*Value (zero-value semantics) ──────────────────────────────────

func TestGetPayloadStringValue(t *testing.T) {
	if v := GetPayloadStringValue(map[string]*pb.Value{"x": Nv("hello")}, "x"); v != "hello" {
		t.Errorf("expected 'hello', got %q", v)
	}
	if v := GetPayloadStringValue(nil, "x"); v != "" {
		t.Errorf("expected empty string, got %q", v)
	}
}

func TestGetPayloadFloatValue(t *testing.T) {
	if v := GetPayloadFloatValue(map[string]*pb.Value{"x": Nv(3.14)}, "x"); v != 3.14 {
		t.Errorf("expected 3.14, got %f", v)
	}
	if v := GetPayloadFloatValue(nil, "x"); v != 0 {
		t.Errorf("expected 0, got %f", v)
	}
}

func TestGetPayloadBoolValue(t *testing.T) {
	if v := GetPayloadBoolValue(map[string]*pb.Value{"x": Nv(true)}, "x"); !v {
		t.Error("expected true")
	}
	if v := GetPayloadBoolValue(nil, "x"); v {
		t.Error("expected false")
	}
}

func TestGetPayloadIntValue(t *testing.T) {
	if v := GetPayloadIntValue(map[string]*pb.Value{"x": Nv(float64(99))}, "x"); v != 99 {
		t.Errorf("expected 99, got %d", v)
	}
	if v := GetPayloadIntValue(nil, "x"); v != 0 {
		t.Errorf("expected 0, got %d", v)
	}
}
