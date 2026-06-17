#!/usr/bin/env python3

import argparse
import binascii
import json
from pathlib import Path

import msgpack


PRIMITIVES = {"null", "boolean", "int", "long", "float", "double", "bytes", "string"}


def load_registry(schema_path: str):
    raw = json.loads(Path(schema_path).read_text())
    schemas = raw if isinstance(raw, list) else [raw]
    registry = {}
    for schema in schemas:
        collect_named_records(schema, registry)
    return registry


def collect_named_records(schema, registry):
    if isinstance(schema, list):
        for item in schema:
            collect_named_records(item, registry)
        return
    if not isinstance(schema, dict):
        return
    if schema.get("type") == "record":
        name = schema.get("name", "")
        namespace = schema.get("namespace", "")
        full_name = f"{namespace}.{name}" if namespace and "." not in name else name
        if full_name:
            registry[full_name] = schema
        if name:
            registry[name] = schema
    for value in schema.values():
        collect_named_records(value, registry)


def resolve_schema(schema, registry):
    if isinstance(schema, str):
        return registry.get(schema, schema)
    return schema


def restore_value(schema, value, registry):
    schema = resolve_schema(schema, registry)
    if isinstance(schema, str):
        return value
    schema_type = schema.get("type")
    if isinstance(schema_type, list):
        if value is None:
            return None
        non_null = next((item for item in schema_type if item != "null"), "null")
        return restore_value(non_null, value, registry)
    if schema_type == "array":
        return [restore_value(schema["items"], item, registry) for item in value]
    if schema_type == "map":
        return {str(k): restore_value(schema["values"], v, registry) for k, v in value.items()}
    if schema_type == "record":
        if isinstance(value, list):
            out = {}
            for field in schema.get("fields", []):
                key = field.get("msgpack_key", field["name"])
                if isinstance(key, int) and key < len(value):
                    raw = value[key]
                    if raw is not None:
                        out[field["name"]] = restore_value(field["type"], raw, registry)
            return out
        if isinstance(value, dict):
            out = dict(value)
            for field in schema.get("fields", []):
                key = field.get("msgpack_key", field["name"])
                raw = value.get(str(key)) if isinstance(key, int) else value.get(key)
                if raw is not None:
                    out[field["name"]] = restore_value(field["type"], raw, registry)
            return out
    return value


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--schema", required=True, help="Avro schema JSON file")
    parser.add_argument("--class", dest="class_name", required=True, help="Root class name")
    parser.add_argument("--hex", dest="hex_data", help="Compact msgpack bytes as hex string")
    args = parser.parse_args()

    registry = load_registry(args.schema)
    if args.class_name not in registry:
        raise SystemExit(f"class not found: {args.class_name}")

    payload = msgpack.unpackb(binascii.unhexlify(args.hex_data), raw=False, strict_map_key=False)
    restored = restore_value(registry[args.class_name], payload, registry)
    print(json.dumps(restored, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
