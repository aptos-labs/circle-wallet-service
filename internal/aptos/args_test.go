package aptos

import (
	"encoding/hex"
	"testing"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
)

func TestIsObjectType(t *testing.T) {
	cases := []struct {
		name    string
		typeStr string
		want    bool
	}{
		{"short-form 0x1", "0x1::object::Object<0x1::fungible_asset::Metadata>", true},
		{"short-form 0x01", "0x01::object::Object<T>", true},
		{
			name:    "long-form 64-hex",
			typeStr: "0x0000000000000000000000000000000000000000000000000000000000000001::object::Object<0x1::coin::Coin<0x1::aptos_coin::AptosCoin>>",
			want:    true,
		},
		{"no type parameter", "0x1::object::Object", true},
		{"different address", "0xbeef::object::Object<T>", false},
		{"different module", "0x1::Object::Object<T>", false}, // case-sensitive
		{"different struct name", "0x1::object::ObjectCore", false},
		{"plain struct under 0x1", "0x1::coin::Coin<T>", false},
		{"vector prefix", "vector<0x1::object::Object<T>>", false}, // caller unwraps vectors
		{"primitive", "address", false},
		{"malformed", "not::enough", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isObjectType(tc.typeStr); got != tc.want {
				t.Fatalf("isObjectType(%q) = %v, want %v", tc.typeStr, got, tc.want)
			}
		})
	}
}

// TestSerializeArgument_Object verifies that an Object<T> argument serializes
// to exactly the same bytes as a plain address, regardless of whether the ABI
// reports the 0x1 in short or long form.
func TestSerializeArgument_Object(t *testing.T) {
	const addrStr = "0x1234"
	wantBytes, err := bcs.Serialize(mustAddr(t, addrStr))
	if err != nil {
		t.Fatalf("serialize address: %v", err)
	}

	types := []string{
		"address",
		"0x1::object::Object<0x1::fungible_asset::Metadata>",
		"0x0000000000000000000000000000000000000000000000000000000000000001::object::Object<T>",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			got, err := SerializeArgument(typ, addrStr)
			if err != nil {
				t.Fatalf("SerializeArgument(%q): %v", typ, err)
			}
			if hex.EncodeToString(got) != hex.EncodeToString(wantBytes) {
				t.Fatalf("type %q: got %x, want %x", typ, got, wantBytes)
			}
		})
	}
}

// TestSerializeArgument_VectorOfObjects ensures the vector<Object<T>> path
// recurses correctly and produces ULEB128 length + concatenated 32-byte
// addresses.
func TestSerializeArgument_VectorOfObjects(t *testing.T) {
	addrs := []any{"0x1", "0x2"}
	got, err := SerializeArgument("vector<0x1::object::Object<T>>", addrs)
	if err != nil {
		t.Fatalf("SerializeArgument: %v", err)
	}
	// ULEB128(2) = 0x02, followed by two 32-byte addresses = 1 + 64 = 65 bytes.
	if len(got) != 1+2*32 {
		t.Fatalf("unexpected length %d, want 65: %x", len(got), got)
	}
	if got[0] != 0x02 {
		t.Fatalf("expected ULEB128 length prefix 0x02, got 0x%02x", got[0])
	}
}

func mustAddr(t *testing.T, s string) *aptossdk.AccountAddress {
	t.Helper()
	var a aptossdk.AccountAddress
	if err := a.ParseStringRelaxed(s); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &a
}
