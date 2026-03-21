# Haruki-Nuverse-StructTool
Nuverse regions msgpack structure file extractor and generator.

Using [custom Avro schema](https://github.com/middlered/unity-msgpack-schema-exporter?tab=readme-ov-file#custom-avro-fields) format to restore stripped msgpack format.


### CLI Build

```bash
go build
```

### CLI Usage

```bash
go run . --schema <schema.avsc.json> --class <ClassName> --hex <hex>
```

### Lib Test
```bash
cd avro
go test -v
```

### Generate Avro schema

See the [exporter repo cli readme](https://github.com/middlered/unity-msgpack-schema-exporter?tab=readme-ov-file#cli)
