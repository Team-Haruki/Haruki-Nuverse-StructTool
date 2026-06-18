// https://github.com/middlered/unity-msgpack-schema-exporter/tree/master/examples/go

package avro_parser

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	msgpack "github.com/vmihailenco/msgpack/v5"
)

// region：Schema representation

// Schema is the parsed Avro schema tree, enriched with our custom fields.
type Schema struct {
	Type      string             // "record", "array", "map", "union", "null", "string", "int", …
	Name      string             // fully-qualified record name
	Fields    []*Field           // for "record"
	Items     *Schema            // for "array"
	Values    *Schema            // for "map"
	KeyType   string             // for "map": value of msgpack_key_type ("int","long","bytes",…)
	UnionOf   []*Schema          // for "union" (Avro union)
	UnionDisp []*UnionVariant    // for records with msgpack_unions
	registry  map[string]*Schema // back-ref for resolving named types by full name
}

// Field is a record field, including its msgpack key.
type Field struct {
	Name       string
	Type       *Schema
	MsgpackKey interface{} // int or string
}

// UnionVariant is one entry in msgpack_unions.
type UnionVariant struct {
	Key  int    // discriminator value written to msgpack
	Type string // short type name
}

// region: Registry – holds all parsed schemas by name

// Registry maps fully-qualified names to their schemas.
type Registry map[string]*Schema

// LoadFile parses an Avro schema JSON file and returns the root schema.
// The file may be a single schema object or an array of schemas.
func LoadFile(path string) (Registry, *Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadBytes parses Avro schema JSON bytes.
func LoadBytes(data []byte) (Registry, *Schema, error) {
	reg := make(Registry)
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, err
	}

	var root *Schema
	// Handle top-level array (avro --all output) or single object.
	if len(raw) > 0 && raw[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, nil, err
		}
		for _, item := range arr {
			s, err := parseSchema(item, reg)
			if err != nil {
				return nil, nil, err
			}
			if root == nil {
				root = s
			}
		}
	} else {
		s, err := parseSchema(raw, reg)
		if err != nil {
			return nil, nil, err
		}
		root = s
	}

	// Patch registry reference into every schema for named-type resolution.
	for _, s := range reg {
		s.registry = reg
		for _, f := range s.Fields {
			patchRegistry(f.Type, reg)
		}
	}

	return reg, root, nil
}

func patchRegistry(s *Schema, reg Registry) {
	if s == nil {
		return
	}
	s.registry = reg
	for _, f := range s.Fields {
		patchRegistry(f.Type, reg)
	}
	patchRegistry(s.Items, reg)
	patchRegistry(s.Values, reg)
	for _, u := range s.UnionOf {
		patchRegistry(u, reg)
	}
}

// region: JSON to Schema parser

func parseSchema(raw json.RawMessage, reg Registry) (*Schema, error) {
	if len(raw) == 0 {
		return &Schema{Type: "null"}, nil
	}
	switch raw[0] {
	case '"':
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, err
		}
		return primitiveOrRef(name, reg), nil

	case '[':
		return parseUnionArray(raw, reg)

	case '{':
		return parseObject(raw, reg)

	default:
		return &Schema{Type: "null"}, nil
	}
}

func primitiveOrRef(name string, reg Registry) *Schema {
	switch name {
	case "null", "boolean", "int", "long", "float", "double", "bytes", "string":
		return &Schema{Type: name}
	default:
		if s, ok := reg[name]; ok {
			return s
		}
		// Forward reference; will resolve later via registry.
		return &Schema{Type: "ref", Name: name}
	}
}

func parseUnionArray(raw json.RawMessage, reg Registry) (*Schema, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	s := &Schema{Type: "union"}
	for _, item := range items {
		child, err := parseSchema(item, reg)
		if err != nil {
			return nil, err
		}
		s.UnionOf = append(s.UnionOf, child)
	}
	return s, nil
}

func parseObject(raw json.RawMessage, reg Registry) (*Schema, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	typeRaw, ok := obj["type"]
	if !ok {
		return &Schema{Type: "null"}, nil
	}
	var typeName string
	// type may be a string or an array (union shorthand).
	if typeRaw[0] == '[' {
		return parseUnionArray(typeRaw, reg)
	}
	json.Unmarshal(typeRaw, &typeName) //nolint:errcheck

	switch typeName {
	case "record":
		return parseRecord(obj, reg)
	case "array":
		return parseArray(obj, reg)
	case "map":
		return parseMap(obj, reg)
	default:
		return primitiveOrRef(typeName, reg), nil
	}
}

func parseRecord(obj map[string]json.RawMessage, reg Registry) (*Schema, error) {
	s := &Schema{Type: "record"}

	var name, ns string
	json.Unmarshal(obj["name"], &name)    //nolint:errcheck
	json.Unmarshal(obj["namespace"], &ns) //nolint:errcheck
	if ns != "" && !strings.Contains(name, ".") {
		s.Name = ns + "." + name
	} else {
		s.Name = name
	}

	// Parse msgpack_unions if present.
	if unionsRaw, ok := obj["msgpack_unions"]; ok {
		var unions []struct {
			Key  int    `json:"key"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(unionsRaw, &unions); err != nil {
			return nil, fmt.Errorf("msgpack_unions in %s: %w", s.Name, err)
		}
		for _, u := range unions {
			s.UnionDisp = append(s.UnionDisp, &UnionVariant{Key: u.Key, Type: u.Type})
		}
	}

	// Parse fields.
	if fieldsRaw, ok := obj["fields"]; ok {
		var rawFields []json.RawMessage
		if err := json.Unmarshal(fieldsRaw, &rawFields); err != nil {
			return nil, err
		}
		for _, rf := range rawFields {
			f, err := parseField(rf, reg)
			if err != nil {
				return nil, err
			}
			s.Fields = append(s.Fields, f)
		}
	}

	if s.Name != "" {
		reg[s.Name] = s
		// Also register short name for convenience.
		parts := strings.Split(s.Name, ".")
		reg[parts[len(parts)-1]] = s
	}
	return s, nil
}

func parseField(raw json.RawMessage, reg Registry) (*Field, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	f := &Field{}
	json.Unmarshal(obj["name"], &f.Name) //nolint:errcheck

	typeSchema, err := parseSchema(obj["type"], reg)
	if err != nil {
		return nil, fmt.Errorf("field %s type: %w", f.Name, err)
	}
	f.Type = typeSchema

	// msgpack_key is int or string.
	if keyRaw, ok := obj["msgpack_key"]; ok {
		if keyRaw[0] == '"' {
			var s string
			json.Unmarshal(keyRaw, &s) //nolint:errcheck
			f.MsgpackKey = s
		} else {
			var n int
			json.Unmarshal(keyRaw, &n) //nolint:errcheck
			f.MsgpackKey = n
		}
	} else {
		f.MsgpackKey = f.Name
	}

	return f, nil
}

func parseArray(obj map[string]json.RawMessage, reg Registry) (*Schema, error) {
	s := &Schema{Type: "array"}
	if itemsRaw, ok := obj["items"]; ok {
		items, err := parseSchema(itemsRaw, reg)
		if err != nil {
			return nil, err
		}
		s.Items = items
	}
	return s, nil
}

func parseMap(obj map[string]json.RawMessage, reg Registry) (*Schema, error) {
	s := &Schema{Type: "map"}
	if valRaw, ok := obj["values"]; ok {
		vals, err := parseSchema(valRaw, reg)
		if err != nil {
			return nil, err
		}
		s.Values = vals
	}
	if ktRaw, ok := obj["msgpack_key_type"]; ok {
		json.Unmarshal(ktRaw, &s.KeyType) //nolint:errcheck
	}
	return s, nil
}

// resolve follows "ref" schemas to their real definition.
func (s *Schema) resolve() *Schema {
	if s.Type == "ref" {
		if real, ok := s.registry[s.Name]; ok {
			return real
		}
	}
	return s
}

// region: Decoder: compact msgpack bytes -> Go value

// Decode decodes a compact msgpack byte slice into a Go value according to
// the given schema. The returned value is one of:
//
//	nil          – null
//	bool
//	int64        – any integer type
//	float64      – float/double
//	string
//	[]byte       – bytes
//	[]any        – array
//	map[string]any – record (field names as keys) or Avro map with string keys
//	map[any]any  – Avro map with non-string keys (msgpack_key_type set)
func Decode(schema *Schema, data []byte) (any, error) {
	dec := msgpack.NewDecoder(strings.NewReader(string(data)))
	dec.SetCustomStructTag("msgpack")
	dec.UseLooseInterfaceDecoding(true)
	return decodeValue(schema, dec)
}

func decodeValue(schema *Schema, dec *msgpack.Decoder) (any, error) {
	if schema == nil {
		return dec.DecodeInterface()
	}
	schema = schema.resolve()

	switch schema.Type {
	case "null":
		return nil, dec.DecodeNil()

	case "boolean":
		return dec.DecodeBool()

	case "int", "long":
		return dec.DecodeInt64()

	case "float", "double":
		return dec.DecodeFloat64()

	case "string":
		return dec.DecodeString()

	case "bytes":
		return dec.DecodeBytes()

	case "record":
		return decodeRecord(schema, dec)

	case "array":
		return decodeArray(schema, dec)

	case "map":
		return decodeMap(schema, dec)

	case "union":
		return decodeUnion(schema, dec)

	default:
		return dec.DecodeInterface()
	}
}

func decodeRecord(schema *Schema, dec *msgpack.Decoder) (any, error) {
	// Peek at what kind of value the msgpack contains.
	// Compact (int-keyed) records are msgpack arrays; string-keyed records are maps.
	code, err := dec.PeekCode()
	if err != nil {
		return nil, err
	}

	// Discriminate: if it's a 2-element array [int, payload], this is a union value.
	if len(schema.UnionDisp) > 0 {
		return decodeUnionDispatch(schema, dec)
	}

	if isArrayCode(code) {
		return decodeIntKeyedRecord(schema, dec)
	}
	return decodeStringKeyedRecord(schema, dec)
}

// decodeIntKeyedRecord decodes an array-format msgpack record.
// The array index matches the msgpack_key int of each field.
func decodeIntKeyedRecord(schema *Schema, dec *msgpack.Decoder) (any, error) {
	length, err := dec.DecodeArrayLen()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}

	// Build index -> field map.
	byIndex := make(map[int]*Field, len(schema.Fields))
	for _, f := range schema.Fields {
		if idx, ok := f.MsgpackKey.(int); ok {
			byIndex[idx] = f
		}
	}

	result := make(map[string]any, length)
	for i := 0; i < length; i++ {
		if f, ok := byIndex[i]; ok {
			v, err := decodeValue(f.Type, dec)
			if err != nil {
				return nil, fmt.Errorf("field index %d: %w", i, err)
			}
			result[f.Name] = v
		} else {
			// Unknown index – skip.
			if _, err := dec.DecodeInterface(); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// decodeStringKeyedRecord decodes a map-format msgpack record.
func decodeStringKeyedRecord(schema *Schema, dec *msgpack.Decoder) (any, error) {
	length, err := dec.DecodeMapLen()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}

	byKey := make(map[string]*Field, len(schema.Fields))
	for _, f := range schema.Fields {
		switch k := f.MsgpackKey.(type) {
		case string:
			byKey[k] = f
		case int:
			byKey[strconv.Itoa(k)] = f
		}
	}

	result := make(map[string]any, length)
	for i := 0; i < length; i++ {
		key, err := dec.DecodeString()
		if err != nil {
			return nil, fmt.Errorf("record key #%d: %w", i, err)
		}
		if f, ok := byKey[key]; ok {
			v, err := decodeValue(f.Type, dec)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", key, err)
			}
			result[f.Name] = v
		} else {
			v, err := dec.DecodeInterface()
			if err != nil {
				return nil, err
			}
			result[key] = v
		}
	}
	return result, nil
}

// decodeUnionDispatch handles records marked with msgpack_unions.
// On the wire these are [discriminator, payload] 2-element arrays.
func decodeUnionDispatch(schema *Schema, dec *msgpack.Decoder) (any, error) {
	length, err := dec.DecodeArrayLen()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}
	if length != 2 {
		return nil, fmt.Errorf("union dispatch: expected 2-element array, got %d", length)
	}

	discriminator, err := dec.DecodeInt()
	if err != nil {
		return nil, fmt.Errorf("union discriminator: %w", err)
	}

	// Find the variant schema.
	var variantSchema *Schema
	for _, v := range schema.UnionDisp {
		if v.Key == discriminator {
			if schema.registry != nil {
				variantSchema = schema.registry[v.Type]
			}
			break
		}
	}

	payload, err := decodeValue(variantSchema, dec)
	if err != nil {
		return nil, fmt.Errorf("union payload (key=%d): %w", discriminator, err)
	}
	return map[string]any{
		"__type": discriminator,
		"value":  payload,
	}, nil
}

func decodeArray(schema *Schema, dec *msgpack.Decoder) (any, error) {
	length, err := dec.DecodeArrayLen()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}
	result := make([]any, 0, length)
	for i := 0; i < length; i++ {
		v, err := decodeValue(schema.Items, dec)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		result = append(result, v)
	}
	return result, nil
}

func decodeMap(schema *Schema, dec *msgpack.Decoder) (any, error) {
	length, err := dec.DecodeMapLen()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}

	if schema.KeyType == "" {
		// Standard string-keyed map.
		result := make(map[string]any, length)
		for i := 0; i < length; i++ {
			key, err := dec.DecodeString()
			if err != nil {
				return nil, fmt.Errorf("map key #%d: %w", i, err)
			}
			// The key is stored as a JSON-stringified original value per the schema spec;
			// decode it back to the native type.
			nativeKey := key // keep as string if no key_type
			val, err := decodeValue(schema.Values, dec)
			if err != nil {
				return nil, fmt.Errorf("map value[%s]: %w", key, err)
			}
			result[nativeKey] = val
		}
		return result, nil
	}

	// Non-string key map: keys were stringified as JSON before storing.
	result := make(map[any]any, length)
	for i := 0; i < length; i++ {
		rawKey, err := dec.DecodeString()
		if err != nil {
			return nil, fmt.Errorf("map key #%d: %w", i, err)
		}
		nativeKey, err := parseKeyType(rawKey, schema.KeyType)
		if err != nil {
			return nil, fmt.Errorf("parse key %q as %s: %w", rawKey, schema.KeyType, err)
		}
		val, err := decodeValue(schema.Values, dec)
		if err != nil {
			return nil, fmt.Errorf("map value[%v]: %w", nativeKey, err)
		}
		result[nativeKey] = val
	}
	return result, nil
}

func decodeUnion(schema *Schema, dec *msgpack.Decoder) (any, error) {
	code, err := dec.PeekCode()
	if err != nil {
		return nil, err
	}
	// Try null first.
	if isNilCode(code) {
		if err := dec.DecodeNil(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	// Pick the first non-null variant.
	for _, variant := range schema.UnionOf {
		variant = variant.resolve()
		if variant.Type != "null" {
			return decodeValue(variant, dec)
		}
	}
	return dec.DecodeInterface()
}

// region: Encoder: Go value -> compact msgpack bytes

// Encode encodes a Go value into compact msgpack bytes according to the schema.
// The input should be the same structure as returned by Decode.
func Encode(schema *Schema, value any) ([]byte, error) {
	var buf strings.Builder
	enc := msgpack.NewEncoder(&buf)
	if err := encodeValue(schema, enc, value); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

func encodeValue(schema *Schema, enc *msgpack.Encoder, value any) error {
	if schema == nil {
		return enc.Encode(value)
	}
	schema = schema.resolve()

	if value == nil {
		return enc.EncodeNil()
	}

	switch schema.Type {
	case "null":
		return enc.EncodeNil()

	case "boolean":
		b, _ := toBool(value)
		return enc.EncodeBool(b)

	case "int", "long":
		n, _ := toInt64(value)
		return enc.EncodeInt(n)

	case "float", "double":
		f, _ := toFloat64(value)
		return enc.EncodeFloat64(f)

	case "string":
		s, _ := toString(value)
		return enc.EncodeString(s)

	case "bytes":
		switch v := value.(type) {
		case []byte:
			return enc.EncodeBytes(v)
		case string:
			return enc.EncodeBytes([]byte(v))
		}
		return enc.EncodeNil()

	case "record":
		return encodeRecord(schema, enc, value)

	case "array":
		return encodeArray(schema, enc, value)

	case "map":
		return encodeMap(schema, enc, value)

	case "union":
		return encodeUnion(schema, enc, value)

	default:
		return enc.Encode(value)
	}
}

func encodeRecord(schema *Schema, enc *msgpack.Encoder, value any) error {
	m, ok := value.(map[string]any)
	if !ok {
		return enc.EncodeNil()
	}

	// Union dispatch records.
	if len(schema.UnionDisp) > 0 {
		discriminator, _ := toInt64(m["__type"])
		payload := m["value"]
		var variantSchema *Schema
		for _, v := range schema.UnionDisp {
			if int64(v.Key) == discriminator {
				if schema.registry != nil {
					variantSchema = schema.registry[v.Type]
				}
				break
			}
		}
		if err := enc.EncodeArrayLen(2); err != nil {
			return err
		}
		if err := enc.EncodeInt(discriminator); err != nil {
			return err
		}
		return encodeValue(variantSchema, enc, payload)
	}

	// Determine if int-keyed: at least one field has an int MsgpackKey.
	intKeyed := false
	for _, f := range schema.Fields {
		if _, ok := f.MsgpackKey.(int); ok {
			intKeyed = true
			break
		}
	}

	if intKeyed {
		// Find highest key to determine array length.
		maxIdx := -1
		for _, f := range schema.Fields {
			if idx, ok := f.MsgpackKey.(int); ok && idx > maxIdx {
				maxIdx = idx
			}
		}
		if err := enc.EncodeArrayLen(maxIdx + 1); err != nil {
			return err
		}
		byIdx := make(map[int]*Field, len(schema.Fields))
		for _, f := range schema.Fields {
			if idx, ok := f.MsgpackKey.(int); ok {
				byIdx[idx] = f
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if f, ok := byIdx[i]; ok {
				if err := encodeValue(f.Type, enc, m[f.Name]); err != nil {
					return fmt.Errorf("field %s: %w", f.Name, err)
				}
			} else {
				if err := enc.EncodeNil(); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// String-keyed.
	if err := enc.EncodeMapLen(len(schema.Fields)); err != nil {
		return err
	}
	for _, f := range schema.Fields {
		key, _ := f.MsgpackKey.(string)
		if key == "" {
			key = f.Name
		}
		if err := enc.EncodeString(key); err != nil {
			return err
		}
		if err := encodeValue(f.Type, enc, m[f.Name]); err != nil {
			return fmt.Errorf("field %s: %w", f.Name, err)
		}
	}
	return nil
}

func encodeArray(schema *Schema, enc *msgpack.Encoder, value any) error {
	arr, ok := toSlice(value)
	if !ok {
		return enc.EncodeNil()
	}
	if err := enc.EncodeArrayLen(len(arr)); err != nil {
		return err
	}
	for i, v := range arr {
		if err := encodeValue(schema.Items, enc, v); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

func encodeMap(schema *Schema, enc *msgpack.Encoder, value any) error {
	if schema.KeyType != "" {
		// Non-string key map – value should be map[any]any.
		m, ok := value.(map[any]any)
		if !ok {
			return enc.EncodeNil()
		}
		if err := enc.EncodeMapLen(len(m)); err != nil {
			return err
		}
		for k, v := range m {
			keyStr := stringifyKey(k)
			if err := enc.EncodeString(keyStr); err != nil {
				return err
			}
			if err := encodeValue(schema.Values, enc, v); err != nil {
				return err
			}
		}
		return nil
	}

	m, ok := value.(map[string]any)
	if !ok {
		return enc.EncodeNil()
	}
	if err := enc.EncodeMapLen(len(m)); err != nil {
		return err
	}
	for k, v := range m {
		if err := enc.EncodeString(k); err != nil {
			return err
		}
		if err := encodeValue(schema.Values, enc, v); err != nil {
			return err
		}
	}
	return nil
}

func encodeUnion(schema *Schema, enc *msgpack.Encoder, value any) error {
	if value == nil {
		return enc.EncodeNil()
	}
	for _, variant := range schema.UnionOf {
		variant = variant.resolve()
		if variant.Type != "null" {
			return encodeValue(variant, enc, value)
		}
	}
	return enc.Encode(value)
}

// region: Utilities

// parseKeyType converts a JSON-stringified key back to its native Go type.
func parseKeyType(s, keyType string) (any, error) {
	switch keyType {
	case "int":
		return strconv.Atoi(s)
	case "long":
		return strconv.ParseInt(s, 10, 64)
	case "float":
		f, err := strconv.ParseFloat(s, 32)
		return float32(f), err
	case "double":
		return strconv.ParseFloat(s, 64)
	case "boolean":
		return strconv.ParseBool(s)
	case "bytes":
		// Keys that are bytes were escaped as \uXXXX sequences.
		return unescapeBytes(s), nil
	default:
		return s, nil
	}
}

// stringifyKey converts a native key to its JSON-string representation.
func stringifyKey(k any) string {
	switch v := k.(type) {
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []byte:
		return escapeBytes(v)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", k)
	}
}

func escapeBytes(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		fmt.Fprintf(&sb, "\\u%04X", c)
	}
	return sb.String()
}

func unescapeBytes(s string) []byte {
	var out []byte
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+5 < len(s) && s[i+1] == 'u' {
			n, err := strconv.ParseUint(s[i+2:i+6], 16, 8)
			if err == nil {
				out = append(out, byte(n))
				i += 6
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return out
}

// msgpack code helpers (from MessagePack spec).
func isArrayCode(code byte) bool {
	return (code >= 0x90 && code <= 0x9f) || code == 0xdc || code == 0xdd
}

func isNilCode(code byte) bool {
	return code == 0xc0
}

func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case int64:
		return x != 0, true
	}
	return false, false
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), true
	case uint64:
		return int64(x), true //nolint:gosec
	case float64:
		return int64(x), true
	case float32:
		return int64(x), true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return math.NaN(), false
}

func toString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return fmt.Sprintf("%v", v), true
}

func toSlice(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	}
	return nil, false
}
