import json
import tempfile
import unittest
from pathlib import Path

import msgpack

from avro_parser import Decode, Encode, LoadBytes, LoadFile, Schema
from avro_parser import schema as schema_module


class DecodeRecordTests(unittest.TestCase):
    def test_union_dispatch_record(self):
        registry, _ = LoadBytes(
            json.dumps(
                [
                    {
                        "type": "record",
                        "name": "Child",
                        "namespace": "Test",
                        "fields": [{"name": "value", "type": "int", "msgpack_key": "value"}],
                    },
                    {
                        "type": "record",
                        "name": "Base",
                        "namespace": "Test",
                        "fields": [],
                        "msgpack_unions": [{"key": 7, "type": "Child"}],
                    },
                ]
            ).encode()
        )

        encoded = Encode(registry["Base"], {"__type": 7, "value": {"value": 42}})
        decoded = Decode(registry["Base"], encoded)

        self.assertEqual(decoded, {"__type": 7, "value": {"value": 42}})

    def test_array_record(self):
        registry, _ = LoadBytes(
            json.dumps(
                {
                    "type": "record",
                    "name": "ArrayRecord",
                    "namespace": "Test",
                    "fields": [
                        {"name": "id", "type": "int", "msgpack_key": 0},
                        {"name": "label", "type": "string", "msgpack_key": 1},
                    ],
                }
            ).encode()
        )

        encoded = Encode(registry["ArrayRecord"], {"id": 9, "label": "item"})
        decoded = Decode(registry["ArrayRecord"], encoded)

        self.assertEqual(decoded, {"id": 9, "label": "item"})

    def test_dict_record_normalizes_keys_and_preserves_unknown_values(self):
        registry, _ = LoadBytes(
            json.dumps(
                {
                    "type": "record",
                    "name": "DictRecord",
                    "namespace": "Test",
                    "fields": [
                        {"name": "id", "type": "int", "msgpack_key": "Id"},
                        {"name": "label", "type": "string", "msgpack_key": 1},
                    ],
                }
            ).encode()
        )
        payload = {"Id": 5, "1": "item", "unknown": True}

        decoded = Decode(registry["DictRecord"], msgpack.packb(payload, use_bin_type=True))

        self.assertEqual(decoded, {"id": 5, "label": "item", "unknown": True})

    def test_nested_collections_and_nullable_union_roundtrip(self):
        registry, _ = LoadBytes(
            json.dumps(
                {
                    "type": "record",
                    "name": "Container",
                    "namespace": "Test",
                    "fields": [
                        {"name": "values", "type": {"type": "array", "items": "long"}},
                        {
                            "name": "flags",
                            "type": {"type": "map", "values": "boolean", "msgpack_key_type": "int"},
                        },
                        {"name": "note", "type": ["null", "string"]},
                    ],
                }
            ).encode()
        )
        value = {"values": [1, 2], "flags": {3: True}, "note": "ready"}

        decoded = Decode(registry["Container"], Encode(registry["Container"], value))

        self.assertEqual(decoded, value)
        value["note"] = None
        self.assertEqual(Decode(registry["Container"], Encode(registry["Container"], value)), value)

    def test_load_file_and_deferred_reference(self):
        raw = json.dumps(
            [
                {
                    "type": "record",
                    "name": "Outer",
                    "namespace": "Test",
                    "fields": [{"name": "child", "type": "Later"}],
                },
                {
                    "type": "record",
                    "name": "Later",
                    "namespace": "Test",
                    "fields": [{"name": "value", "type": "int"}],
                },
            ]
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "schema.avsc"
            path.write_text(raw)
            registry, root = LoadFile(str(path))

        value = {"child": {"value": 3}}
        decoded = Decode(registry["Outer"], Encode(registry["Outer"], value))

        self.assertEqual(root.name, "Test.Outer")
        self.assertEqual(decoded, value)

    def test_internal_fallbacks_and_key_conversions(self):
        self.assertEqual(schema_module._decode_value(None, "raw"), "raw")
        self.assertEqual(schema_module._decode_record(Schema(type="record"), "raw"), "raw")
        self.assertEqual(schema_module._encode_value(None, "raw"), "raw")
        self.assertEqual(schema_module._encode_value(Schema(type="unknown"), "raw"), "raw")
        self.assertEqual(schema_module._parse_schema(None, {}).type, "null")
        self.assertEqual(schema_module._primitive_or_ref("Missing", {}).type, "ref")

        cases = [
            ("12", "int", 12),
            ("13", "long", 13),
            ("1.5", "float", 1.5),
            ("2.5", "double", 2.5),
            ("TRUE", "boolean", True),
            ("plain", "string", "plain"),
            ('"bytes"', "bytes", "bytes"),
        ]
        for key, key_type, expected in cases:
            with self.subTest(key_type=key_type):
                self.assertEqual(schema_module._parse_key_type(key, key_type), expected)

        self.assertEqual(schema_module._stringify_key(12, ""), "12")
        self.assertEqual(schema_module._stringify_key(True, "boolean"), "True")
        self.assertEqual(schema_module._stringify_key({"x": 1}, "json"), '{"x": 1}')


if __name__ == "__main__":
    unittest.main()
