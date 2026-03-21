package avro_parser

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

const simpleItemSchema = `{
  "type": "record",
  "name": "SimpleItem",
  "namespace": "Test",
  "fields": [
    {"name": "id",     "type": "int",    "msgpack_key": "id"},
    {"name": "name",   "type": ["null","string"], "msgpack_key": "name"},
    {"name": "score",  "type": "float",  "msgpack_key": "score"},
    {"name": "active", "type": "boolean","msgpack_key": "active"}
  ]
}`

const intKeyedSchema = `{
  "type": "record",
  "name": "IntKeyedItem",
  "namespace": "Test",
  "fields": [
    {"name": "id",    "type": "int",    "msgpack_key": 0},
    {"name": "label", "type": ["null","string"], "msgpack_key": 1},
    {"name": "value", "type": "double", "msgpack_key": 2}
  ]
}`

const intKeyedDictSchema = `{
  "type": "record",
  "name": "IntKeyedDict",
  "namespace": "Test",
  "fields": [
    {"name": "id",     "type": "string", "msgpack_key": "id"},
    {"name": "scores", "type": {"type": "map", "values": "float", "msgpack_key_type": "int"}, "msgpack_key": "scores"}
  ]
}`

const unionSchema = `[
  {
    "type": "record",
    "name": "UnionChildA",
    "namespace": "Test",
    "fields": [{"name": "x", "type": "int", "msgpack_key": "x"}]
  },
  {
    "type": "record",
    "name": "UnionChildB",
    "namespace": "Test",
    "fields": [{"name": "y", "type": "string", "msgpack_key": "y"}]
  },
  {
    "type": "record",
    "name": "UnionBase",
    "namespace": "Test",
    "fields": [],
    "msgpack_unions": [
      {"key": 0, "type": "UnionChildA"},
      {"key": 1, "type": "UnionChildB"}
    ]
  }
]`

func mustLoad(t *testing.T, schemaJSON string) Registry {
	t.Helper()
	reg, _, err := LoadBytes([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	return reg
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint:
		return int64(x)
	case uint64:
		return int64(x) //nolint:gosec
	case float64:
		return int64(x)
	}
	panic(fmt.Sprintf("not an int: %T %v", v, v))
}

func TestStringKeyedDecode(t *testing.T) {
	reg := mustLoad(t, simpleItemSchema)
	schema := reg["SimpleItem"]

	// {id:1, name:"Sword", score:9.5, active:true}  — string-keyed msgpack map
	// Hex: 84 A2 69 64 01 A4 6E616D65 A5 5377 6F72 64 A5 7363 6F72 65 CA 41180000 A6 616374 697665 C3
	// Let's encode it via the library and decode it back.
	input := map[string]any{
		"id":     int64(1),
		"name":   "Sword",
		"score":  float64(9.5),
		"active": true,
	}
	encoded, err := Encode(schema, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	m := decoded.(map[string]any)
	if toInt(m["id"]) != 1 {
		t.Errorf("id: want 1, got %v", m["id"])
	}
	if m["name"].(string) != "Sword" {
		t.Errorf("name: want Sword, got %v", m["name"])
	}
	if m["active"].(bool) != true {
		t.Errorf("active: want true, got %v", m["active"])
	}
}

func TestStringKeyedRoundtrip(t *testing.T) {
	reg := mustLoad(t, simpleItemSchema)
	schema := reg["SimpleItem"]

	input := map[string]any{
		"id":     int64(42),
		"name":   "Shield",
		"score":  float64(7.0),
		"active": false,
	}
	encoded, err := Encode(schema, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	reEncoded, err := Encode(schema, decoded)
	if err != nil {
		t.Fatalf("re-Encode: %v", err)
	}
	if hex.EncodeToString(encoded) != hex.EncodeToString(reEncoded) {
		t.Errorf("round-trip mismatch:\n  encoded:    %s\n  re-encoded: %s",
			hex.EncodeToString(encoded), hex.EncodeToString(reEncoded))
	}
}

func TestNullableField(t *testing.T) {
	reg := mustLoad(t, simpleItemSchema)
	schema := reg["SimpleItem"]

	input := map[string]any{
		"id":     int64(5),
		"name":   nil, // null
		"score":  float64(0.0),
		"active": false,
	}
	encoded, _ := Encode(schema, input)
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m := decoded.(map[string]any)
	if m["name"] != nil {
		t.Errorf("name should be nil, got %v", m["name"])
	}
}

func TestIntKeyedRoundtrip(t *testing.T) {
	reg := mustLoad(t, intKeyedSchema)
	schema := reg["IntKeyedItem"]

	input := map[string]any{
		"id":    int64(7),
		"label": "hello",
		"value": float64(3.14),
	}
	encoded, err := Encode(schema, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Compact form is a msgpack array.
	if encoded[0]&0xf0 != 0x90 {
		t.Errorf("expected array-format msgpack, first byte=0x%02X", encoded[0])
	}

	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m := decoded.(map[string]any)
	if toInt(m["id"]) != 7 {
		t.Errorf("id: want 7, got %v", m["id"])
	}
	if m["label"].(string) != "hello" {
		t.Errorf("label: want hello, got %v", m["label"])
	}
}

func TestIntKeyedNullLabel(t *testing.T) {
	reg := mustLoad(t, intKeyedSchema)
	schema := reg["IntKeyedItem"]

	input := map[string]any{
		"id":    int64(3),
		"label": nil,
		"value": float64(1.0),
	}
	encoded, _ := Encode(schema, input)
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m := decoded.(map[string]any)
	if m["label"] != nil {
		t.Errorf("label should be nil, got %v", m["label"])
	}
}

func TestIntKeyMapRoundtrip(t *testing.T) {
	reg := mustLoad(t, intKeyedDictSchema)
	schema := reg["IntKeyedDict"]

	scoreMap := map[any]any{int64(10): float64(1.5), int64(20): float64(2.5)}
	input := map[string]any{
		"id":     "player1",
		"scores": scoreMap,
	}
	encoded, err := Encode(schema, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m := decoded.(map[string]any)
	if m["id"].(string) != "player1" {
		t.Errorf("id: want player1, got %v", m["id"])
	}
	scores := m["scores"].(map[any]any)
	if len(scores) != 2 {
		t.Errorf("scores len: want 2, got %d", len(scores))
	}
}

func TestUnionDispatchChildA(t *testing.T) {
	reg := mustLoad(t, unionSchema)
	schema := reg["UnionBase"]

	// Encode as [0, {x: 42}]
	input := map[string]any{
		"__type": int64(0),
		"value":  map[string]any{"x": int64(42)},
	}
	encoded, err := Encode(schema, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	outer := decoded.(map[string]any)
	if toInt(outer["__type"]) != 0 {
		t.Errorf("__type: want 0, got %v", outer["__type"])
	}
	inner := outer["value"].(map[string]any)
	if toInt(inner["x"]) != 42 {
		t.Errorf("x: want 42, got %v", inner["x"])
	}
}

func TestUnionDispatchChildB(t *testing.T) {
	reg := mustLoad(t, unionSchema)
	schema := reg["UnionBase"]

	input := map[string]any{
		"__type": int64(1),
		"value":  map[string]any{"y": "hello"},
	}
	encoded, _ := Encode(schema, input)
	decoded, err := Decode(schema, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	outer := decoded.(map[string]any)
	inner := outer["value"].(map[string]any)
	if inner["y"].(string) != "hello" {
		t.Errorf("y: want hello, got %v", inner["y"])
	}
}

func TestLoadAllSchemas(t *testing.T) {
	schemas := `[
	  {"type":"record","name":"TypeA","namespace":"T","fields":[{"name":"a","type":"int","msgpack_key":"a"}]},
	  {"type":"record","name":"TypeB","namespace":"T","fields":[{"name":"b","type":"string","msgpack_key":"b"}]}
	]`
	reg, _, err := LoadBytes([]byte(schemas))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if reg["T.TypeA"] == nil {
		t.Error("T.TypeA not found")
	}
	if reg["T.TypeB"] == nil {
		t.Error("T.TypeB not found")
	}
}

func TestDecodeProducesJSONSerializable(t *testing.T) {
	reg := mustLoad(t, simpleItemSchema)
	schema := reg["SimpleItem"]

	input := map[string]any{
		"id": int64(99), "name": "Axe", "score": float64(5.0), "active": true,
	}
	encoded, _ := Encode(schema, input)
	decoded, _ := Decode(schema, encoded)
	_, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("decoded value is not JSON serializable: %v", err)
	}
}
