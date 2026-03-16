package aptos

import (
	"testing"
)

func TestParseFunctionID_Valid(t *testing.T) {
	addr, module, fn, err := ParseFunctionID("0x1::contractInt::mint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "0x1" {
		t.Errorf("addr = %q, want %q", addr, "0x1")
	}
	if module != "contractInt" {
		t.Errorf("module = %q, want %q", module, "contractInt")
	}
	if fn != "mint" {
		t.Errorf("function = %q, want %q", fn, "mint")
	}
}

func TestParseFunctionID_LongAddress(t *testing.T) {
	id := "0x0000000000000000000000000000000000000000000000000000000000000001::coin::transfer"
	addr, module, fn, err := ParseFunctionID(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "0x0000000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("addr = %q", addr)
	}
	if module != "coin" || fn != "transfer" {
		t.Errorf("module=%q fn=%q", module, fn)
	}
}

func TestParseFunctionID_Invalid(t *testing.T) {
	tests := []string{
		"",
		"0x1",
		"0x1::module",
		"::module::func",
		"0x1::::func",
		"notanaddr::mod::func",
	}
	for _, tc := range tests {
		_, _, _, err := ParseFunctionID(tc)
		if err == nil {
			t.Errorf("expected error for %q", tc)
		}
	}
}

func TestSerializeArgument_Address(t *testing.T) {
	b, err := SerializeArgument("address", "0x1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("address bytes len = %d, want 32", len(b))
	}
}

func TestSerializeArgument_Bool(t *testing.T) {
	b, err := SerializeArgument("bool", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 1 || b[0] != 1 {
		t.Errorf("bool(true) = %v, want [1]", b)
	}

	b, err = SerializeArgument("bool", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 1 || b[0] != 0 {
		t.Errorf("bool(false) = %v, want [0]", b)
	}
}

func TestSerializeArgument_U8(t *testing.T) {
	b, err := SerializeArgument("u8", float64(42))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 1 || b[0] != 42 {
		t.Errorf("u8(42) = %v", b)
	}
}

func TestSerializeArgument_U64_Float(t *testing.T) {
	b, err := SerializeArgument("u64", float64(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 8 {
		t.Errorf("u64 bytes len = %d, want 8", len(b))
	}
}

func TestSerializeArgument_U64_String(t *testing.T) {
	b, err := SerializeArgument("u64", "18446744073709551615")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 8 {
		t.Errorf("u64 bytes len = %d, want 8", len(b))
	}
	// Max uint64 = all 0xFF bytes
	for _, v := range b {
		if v != 0xFF {
			t.Errorf("expected 0xFF, got 0x%02X", v)
		}
	}
}

func TestSerializeArgument_String(t *testing.T) {
	b, err := SerializeArgument("0x1::string::String", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// BCS string = ULEB128 length + UTF-8 bytes
	// "hello" = 5 bytes, so length byte is 5, total = 6
	if len(b) != 6 {
		t.Errorf("string bytes len = %d, want 6", len(b))
	}
}

func TestSerializeArgument_Vector(t *testing.T) {
	arr := []any{float64(1), float64(2), float64(3)}
	b, err := SerializeArgument("vector<u8>", arr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ULEB128(3) + 3 bytes = 4 bytes
	if len(b) != 4 {
		t.Errorf("vector<u8> bytes len = %d, want 4", len(b))
	}
}

func TestSerializeArgument_Object(t *testing.T) {
	b, err := SerializeArgument("0x1::object::Object<0x1::coin::CoinStore>", "0x1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("object bytes len = %d, want 32", len(b))
	}
}

func TestSerializeArgument_U8Overflow(t *testing.T) {
	_, err := SerializeArgument("u8", float64(256))
	if err == nil {
		t.Error("expected error for u8 overflow")
	}
}

func TestSerializeArgument_UnsupportedType(t *testing.T) {
	_, err := SerializeArgument("some_custom_struct", "value")
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestSerializeArgument_U128(t *testing.T) {
	b, err := SerializeArgument("u128", "340282366920938463463374607431768211455")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 16 {
		t.Errorf("u128 bytes len = %d, want 16", len(b))
	}
}

func TestSerializeArgument_U256(t *testing.T) {
	b, err := SerializeArgument("u256", "0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("u256 bytes len = %d, want 32", len(b))
	}
}
