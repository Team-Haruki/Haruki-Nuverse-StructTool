# Haruki-Nuverse-StructTool
Nuverse regions msgpack structure file extractor and generator

### Build

```bash
go build ./cmd/structtool
```

### Check structure consistency

```bash
go run ./cmd/structtool check -dump dump.cs -structures structures.json -suite-class SuiteMaster
go run ./cmd/structtool check -dump dump.cs -structures suite_structures.json -suite-class SuiteUser
```

### Update structures from `dump.cs`

```bash
go run ./cmd/structtool update -dump dump.cs -structures structures.json -suite-class SuiteMaster -keys cards,events
```

### Extract User msgpack-array report and suite structures

```bash
go run ./cmd/structtool extract-user -dump dump.cs -report-out user_msgpack_array_report.json -suite-out suite_structures.json
```
