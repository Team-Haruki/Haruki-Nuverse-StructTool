import io
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import msgpack


PRIMITIVES = {"null", "boolean", "int", "long", "float", "double", "bytes", "string"}


@dataclass
class UnionVariant:
    key: int
    type: str


@dataclass
class Field:
    name: str
    type: "Schema"
    msgpack_key: Any


@dataclass
class Schema:
    type: str
    name: str = ""
    fields: list[Field] = field(default_factory=list)
    items: "Schema | None" = None
    values: "Schema | None" = None
    key_type: str = ""
    union_of: list["Schema"] = field(default_factory=list)
    union_disp: list[UnionVariant] = field(default_factory=list)
    registry: "Registry | None" = None

    def resolve(self) -> "Schema":
        if self.type == "ref" and self.registry and self.name in self.registry:
            return self.registry[self.name]
        return self


Registry = dict[str, Schema]


def LoadFile(path: str):
    return LoadBytes(Path(path).read_bytes())


def LoadBytes(data: bytes):
    raw = json.loads(data)
    registry: Registry = {}
    root = None
    items = raw if isinstance(raw, list) else [raw]
    for item in items:
        schema = _parse_schema(item, registry)
        if root is None:
            root = schema
    for schema in registry.values():
        _patch_registry(schema, registry)
    return registry, root


def _patch_registry(schema: Schema | None, registry: Registry):
    if schema is None:
        return
    schema.registry = registry
    for field in schema.fields:
        _patch_registry(field.type, registry)
    _patch_registry(schema.items, registry)
    _patch_registry(schema.values, registry)
    for item in schema.union_of:
        _patch_registry(item, registry)


def _parse_schema(raw: Any, registry: Registry) -> Schema:
    if isinstance(raw, str):
        return _primitive_or_ref(raw, registry)
    if isinstance(raw, list):
        return Schema(type="union", union_of=[_parse_schema(item, registry) for item in raw])
    if not isinstance(raw, dict):
        return Schema(type="null")
    schema_type = raw.get("type", "null")
    if isinstance(schema_type, list):
        return _parse_schema(schema_type, registry)
    if schema_type == "record":
        return _parse_record(raw, registry)
    if schema_type == "array":
        return Schema(type="array", items=_parse_schema(raw.get("items"), registry))
    if schema_type == "map":
        return Schema(
            type="map",
            values=_parse_schema(raw.get("values"), registry),
            key_type=raw.get("msgpack_key_type", ""),
        )
    return _primitive_or_ref(schema_type, registry)


def _primitive_or_ref(name: str, registry: Registry) -> Schema:
    if name in PRIMITIVES:
        return Schema(type=name, name=name)
    if name in registry:
        return registry[name]
    return Schema(type="ref", name=name)


def _parse_record(raw: dict[str, Any], registry: Registry) -> Schema:
    name = raw.get("name", "")
    namespace = raw.get("namespace", "")
    full_name = f"{namespace}.{name}" if namespace and "." not in name else name
    schema = Schema(type="record", name=full_name)
    for item in raw.get("msgpack_unions", []):
        schema.union_disp.append(UnionVariant(key=item["key"], type=item["type"]))
    for field_raw in raw.get("fields", []):
        field_type = _parse_schema(field_raw.get("type"), registry)
        field_name = field_raw.get("name", "")
        msgpack_key = field_raw.get("msgpack_key", field_name)
        schema.fields.append(Field(name=field_name, type=field_type, msgpack_key=msgpack_key))
    if full_name:
        registry[full_name] = schema
    if name:
        registry[name] = schema
    return schema


def Decode(schema: Schema, data: bytes):
    value = msgpack.unpackb(data, raw=False, strict_map_key=False)
    return _decode_value(schema, value)


def _decode_value(schema: Schema | None, value: Any):
    if schema is None:
        return value
    schema = schema.resolve()
    if schema.type in {"null", "boolean", "int", "long", "float", "double", "bytes", "string", "ref"}:
        return value
    if schema.type == "record":
        return _decode_record(schema, value)
    if schema.type == "array":
        return [_decode_value(schema.items, item) for item in value]
    if schema.type == "map":
        return {
            _parse_key_type(k, schema.key_type) if schema.key_type else k: _decode_value(schema.values, v)
            for k, v in value.items()
        }
    if schema.type == "union":
        if value is None:
            return None
        non_null = next((item for item in schema.union_of if item.resolve().type != "null"), None)
        return _decode_value(non_null, value)
    return value


def _decode_record(schema: Schema, value: Any):
    if schema.union_disp:
        discriminator = value[0]
        payload_schema = None
        for variant in schema.union_disp:
            if variant.key == discriminator and schema.registry:
                payload_schema = schema.registry.get(variant.type)
                break
        return {"__type": discriminator, "value": _decode_value(payload_schema, value[1])}
    if isinstance(value, list):
        by_index = {field.msgpack_key: field for field in schema.fields if isinstance(field.msgpack_key, int)}
        result = {}
        for idx, item in enumerate(value):
            field = by_index.get(idx)
            if field is not None:
                result[field.name] = _decode_value(field.type, item)
        return result
    if isinstance(value, dict):
        by_key = {}
        for field in schema.fields:
            if isinstance(field.msgpack_key, str):
                by_key[field.msgpack_key] = field
            elif isinstance(field.msgpack_key, int):
                by_key[str(field.msgpack_key)] = field
        result = {}
        consumed = set()
        for key, item in value.items():
            field = by_key.get(str(key))
            if field is not None:
                consumed.add(key)
                result[field.name] = _decode_value(field.type, item)
        for key, item in value.items():
            if key not in consumed:
                result[key] = item
        return result
    return value


def Encode(schema: Schema, value: Any) -> bytes:
    return msgpack.packb(_encode_value(schema, value), use_bin_type=True)


def _encode_value(schema: Schema | None, value: Any):
    if schema is None:
        return value
    schema = schema.resolve()
    if value is None or schema.type in {"null", "boolean", "int", "long", "float", "double", "bytes", "string", "ref"}:
        return value
    if schema.type == "record":
        return _encode_record(schema, value)
    if schema.type == "array":
        return [_encode_value(schema.items, item) for item in value]
    if schema.type == "map":
        return {_stringify_key(k, schema.key_type): _encode_value(schema.values, v) for k, v in value.items()}
    if schema.type == "union":
        non_null = next((item for item in schema.union_of if item.resolve().type != "null"), None)
        return _encode_value(non_null, value)
    return value


def _encode_record(schema: Schema, value: dict[str, Any]):
    if schema.union_disp:
        discriminator = int(value["__type"])
        payload = value["value"]
        payload_schema = None
        for variant in schema.union_disp:
            if variant.key == discriminator and schema.registry:
                payload_schema = schema.registry.get(variant.type)
                break
        return [discriminator, _encode_value(payload_schema, payload)]

    int_fields = [field for field in schema.fields if isinstance(field.msgpack_key, int)]
    if int_fields:
        max_idx = max(int(field.msgpack_key) for field in int_fields)
        out = [None] * (max_idx + 1)
        for field in int_fields:
            out[int(field.msgpack_key)] = _encode_value(field.type, value.get(field.name))
        return out

    out = {}
    for field in schema.fields:
        key = field.msgpack_key if isinstance(field.msgpack_key, str) else field.name
        out[key] = _encode_value(field.type, value.get(field.name))
    return out


def _parse_key_type(key: str, key_type: str):
    if key_type in {"int", "long"}:
        return int(key)
    if key_type in {"float", "double"}:
        return float(key)
    if key_type == "boolean":
        return key.lower() == "true"
    if key_type == "string":
        return key
    return json.loads(key)


def _stringify_key(key: Any, key_type: str):
    if not key_type:
        return str(key)
    if key_type in {"int", "long", "float", "double", "boolean", "string"}:
        return str(key)
    return json.dumps(key, ensure_ascii=False)
