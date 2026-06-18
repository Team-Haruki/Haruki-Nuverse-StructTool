# Haruki-Nuverse-StructTool
Nuverse regions msgpack structure file extractor and generator.

Using [custom Avro schema](https://github.com/middlered/unity-msgpack-schema-exporter?tab=readme-ov-file#custom-avro-fields) format to restore stripped msgpack format.

## Build

```bash
go build
```

## Go CLI Usage

```bash
go run . --schema <schema.avsc.json> --class <ClassName> --hex <hex>
```

Example:

```bash
go run . --schema data/master.avsc --class Sekai.MasterActionSet --hex 910104c0c2c0ad61735f636473686f705f6d6f62c09101a46e6f6e65cf000001577fc7f5b001
```

## Python Example

```bash
python3 -c 'from avro_parser.schema import LoadFile, Decode; reg, root = LoadFile("data/master.avsc"); print(Decode(reg["Sekai.MasterActionSet"], bytes.fromhex("910104c0c2c0ad61735f636473686f705f6d6f62c09101a46e6f6e65cf000001577fc7f5b001")))'
```

## Rust Example

```bash
cd rust/avro_parser
cargo run -- --schema ../../data/master.avsc --class Sekai.MasterActionSet --hex 910104c0c2c0ad61735f636473686f705f6d6f62c09101a46e6f6e65cf000001577fc7f5b001
```

## Included Avro files

This repository now includes two generated schema files:

- `data/master.avsc`: generated `Sekai.Master*` schema set for Nuverse master restore
- `data/suite.avsc`: generated `Sekai.SuiteUser` and `Sekai.User*` schema set for suite or API field restore

Recommended usage:

- use `data/master.avsc` for CDN `master-data-<cdnVersion>.info` payloads
- use `data/suite.avsc` for profile, ranking, and suite-style compact user payloads

## Generate Avro schema

The `.avsc` files are generated from DummyDll metadata, typically `Assembly-CSharp.dll`.

In the Haruki toolchain this is done by reading `MessagePackObject` and `Key` metadata from DummyDll and exporting custom-Avro-style records with `msgpack_key`.

The exporter format reference is here:

- [unity-msgpack-schema-exporter CLI readme](https://github.com/middlered/unity-msgpack-schema-exporter?tab=readme-ov-file#cli)
- [custom Avro fields](https://github.com/middlered/unity-msgpack-schema-exporter?tab=readme-ov-file#custom-avro-fields)

If you already have a DummyDll dump, the general workflow is:

1. Extract or prepare `Assembly-CSharp.dll`
2. Export custom Avro schema JSON
3. Split the output into:
   - `master.avsc` for `Sekai.Master*`
   - `suite.avsc` for `Sekai.SuiteUser` and `Sekai.User*`

In the Haruki-Sekai-API repository, these files are produced from the schema bundle generator and then copied into this repository.

## Test

```bash
go test ./...
cd examples/rust && cargo check
python3 -m py_compile ../python/restore.py
```
