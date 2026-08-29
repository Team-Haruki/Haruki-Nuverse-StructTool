import json
import unittest

import msgpack

from avro_parser import Decode, LoadBytes


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

        decoded = Decode(registry["Base"], msgpack.packb([7, {"value": 42}], use_bin_type=True))

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

        decoded = Decode(registry["ArrayRecord"], msgpack.packb([9, "item"], use_bin_type=True))

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


if __name__ == "__main__":
    unittest.main()
