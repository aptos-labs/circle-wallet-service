package aptos

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
)

func ParseFunctionID(s string) (addr *aptossdk.AccountAddress, module, function string, err error) {
	parts := strings.Split(s, "::")
	if len(parts) != 3 {
		return nil, "", "", fmt.Errorf("invalid function_id %q: expected addr::module::function", s)
	}
	addrStr, module, function := parts[0], parts[1], parts[2]
	if addrStr == "" || module == "" || function == "" {
		return nil, "", "", fmt.Errorf("invalid function_id %q: empty component", s)
	}

	addr = &aptossdk.AccountAddress{}
	err = addr.ParseStringRelaxed(addrStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid function_id address %q: %w", addr, err)
	}
	return addr, module, function, nil
}

func ParseAddress(s string) (aptossdk.AccountAddress, error) {
	var addr aptossdk.AccountAddress
	if err := addr.ParseStringRelaxed(s); err != nil {
		return aptossdk.AccountAddress{}, fmt.Errorf("invalid address %q: %w", s, err)
	}
	return addr, nil
}

func SerializeArgument(typeStr string, value any) ([]byte, error) {
	switch {
	case typeStr == "address" || strings.HasPrefix(typeStr, "0x1::object::Object"):
		return serializeAddress(value)
	case typeStr == "bool":
		return serializeBool(value)
	case typeStr == "u8":
		return serializeUint(value, 8)
	case typeStr == "u16":
		return serializeUint(value, 16)
	case typeStr == "u32":
		return serializeUint(value, 32)
	case typeStr == "u64":
		return serializeUint(value, 64)
	case typeStr == "u128":
		return serializeU128(value)
	case typeStr == "u256":
		return serializeU256(value)
	case typeStr == "0x1::string::String":
		return serializeString(value)
	case strings.HasPrefix(typeStr, "vector<"):
		return serializeVector(typeStr, value)
	default:
		return nil, fmt.Errorf("unsupported Move type %q", typeStr)
	}
}

func ParseTypeTags(tags []string) ([]aptossdk.TypeTag, error) {
	result := make([]aptossdk.TypeTag, len(tags))
	for i, s := range tags {
		tag, err := aptossdk.ParseTypeTag(s)
		if err != nil {
			return nil, fmt.Errorf("type_arguments[%d]: %w", i, err)
		}
		result[i] = *tag
	}
	return result, nil
}

func serializeAddress(value any) ([]byte, error) {
	s, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("address: expected string, got %T", value)
	}
	addr, err := ParseAddress(s)
	if err != nil {
		return nil, fmt.Errorf("address: %w", err)
	}
	return bcs.Serialize(&addr)
}

func serializeBool(value any) ([]byte, error) {
	b, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("bool: expected bool, got %T", value)
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) { ser.Bool(b) })
}

func serializeUint(value any, bits int) ([]byte, error) {
	n, err := toUint64(value)
	if err != nil {
		return nil, fmt.Errorf("u%d: %w", bits, err)
	}
	var maxVal uint64
	switch bits {
	case 8:
		maxVal = math.MaxUint8
	case 16:
		maxVal = math.MaxUint16
	case 32:
		maxVal = math.MaxUint32
	case 64:
		maxVal = math.MaxUint64
	}
	if bits < 64 && n > maxVal {
		return nil, fmt.Errorf("u%d: value %d exceeds max %d", bits, n, maxVal)
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) {
		switch bits {
		case 8:
			ser.U8(uint8(n))
		case 16:
			ser.U16(uint16(n))
		case 32:
			ser.U32(uint32(n))
		case 64:
			ser.U64(n)
		}
	})
}

func serializeU128(value any) ([]byte, error) {
	n, err := toBigInt(value)
	if err != nil {
		return nil, fmt.Errorf("u128: %w", err)
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	if n.Sign() < 0 || n.Cmp(max) > 0 {
		return nil, fmt.Errorf("u128: value out of range")
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) { ser.U128(*n) })
}

func serializeU256(value any) ([]byte, error) {
	n, err := toBigInt(value)
	if err != nil {
		return nil, fmt.Errorf("u256: %w", err)
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if n.Sign() < 0 || n.Cmp(max) > 0 {
		return nil, fmt.Errorf("u256: value out of range")
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) { ser.U256(*n) })
}

func serializeString(value any) ([]byte, error) {
	s, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("string: expected string, got %T", value)
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) { ser.WriteString(s) })
}

func serializeVector(typeStr string, value any) ([]byte, error) {
	inner := typeStr[len("vector<") : len(typeStr)-1]
	arr, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("vector: expected array, got %T", value)
	}
	serialized := make([][]byte, len(arr))
	for i, elem := range arr {
		b, err := SerializeArgument(inner, elem)
		if err != nil {
			return nil, fmt.Errorf("vector[%d]: %w", i, err)
		}
		serialized[i] = b
	}
	return bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.Uleb128(uint32(len(serialized)))
		for _, b := range serialized {
			ser.FixedBytes(b)
		}
	})
}

func toUint64(v any) (uint64, error) {
	switch n := v.(type) {
	case float64:
		if n < 0 || n != math.Trunc(n) {
			return 0, fmt.Errorf("expected non-negative integer, got %v", n)
		}
		return uint64(n), nil
	case string:
		bi, ok := new(big.Int).SetString(n, 10)
		if !ok {
			return 0, fmt.Errorf("expected numeric string, got %q", n)
		}
		if !bi.IsUint64() {
			return 0, fmt.Errorf("value %q overflows uint64", n)
		}
		return bi.Uint64(), nil
	default:
		return 0, fmt.Errorf("expected number or string, got %T", v)
	}
}

func toBigInt(v any) (*big.Int, error) {
	switch n := v.(type) {
	case float64:
		if n < 0 || n != math.Trunc(n) {
			return nil, fmt.Errorf("expected non-negative integer, got %v", n)
		}
		return new(big.Int).SetUint64(uint64(n)), nil
	case string:
		bi, ok := new(big.Int).SetString(n, 10)
		if !ok {
			return nil, fmt.Errorf("expected numeric string, got %q", n)
		}
		return bi, nil
	default:
		return nil, fmt.Errorf("expected number or string, got %T", v)
	}
}
