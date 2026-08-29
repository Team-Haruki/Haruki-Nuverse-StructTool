package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliSchema = `{
  "type": "record",
  "name": "Item",
  "namespace": "Test",
  "fields": [
    {"name": "id", "type": "int", "msgpack_key": "id"}
  ]
}`

func writeCLISchema(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.avsc")
	if err := os.WriteFile(path, []byte(cliSchema), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestRunJSONAndHexRoundtrip(t *testing.T) {
	schemaPath := writeCLISchema(t)
	var encoded bytes.Buffer
	code := run([]string{"-schema", schemaPath, "-class", "Test.Item", "-json", `{"id":7}`}, &encoded)
	if code != 0 {
		t.Fatalf("json run exit code: %d, output: %s", code, encoded.String())
	}

	var decoded bytes.Buffer
	code = run([]string{"-schema", schemaPath, "-class", "Test.Item", "-hex", strings.TrimSpace(encoded.String()), "-v"}, &decoded)
	if code != 0 {
		t.Fatalf("hex run exit code: %d, output: %s", code, decoded.String())
	}
	if !strings.Contains(decoded.String(), `"id": 7`) || !strings.Contains(decoded.String(), "round-trip verification") {
		t.Fatalf("unexpected decoded output: %s", decoded.String())
	}
}

func TestRunFailures(t *testing.T) {
	schemaPath := writeCLISchema(t)
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "flag parse", args: []string{"-unknown"}, code: 2, want: "flag provided but not defined"},
		{name: "missing arguments", code: 1, want: "Usage of structtool"},
		{name: "missing schema", args: []string{"-schema", "missing.avsc", "-class", "Item", "-hex", "00"}, code: 1, want: "failed to load schema"},
		{name: "unknown class", args: []string{"-schema", schemaPath, "-class", "Missing", "-hex", "00"}, code: 1, want: "class not found"},
		{name: "invalid hex", args: []string{"-schema", schemaPath, "-class", "Test.Item", "-hex", "zz"}, code: 1, want: "hex decode failed"},
		{name: "invalid messagepack", args: []string{"-schema", schemaPath, "-class", "Test.Item", "-hex", "c1"}, code: 1, want: "decode failed"},
		{name: "invalid json", args: []string{"-schema", schemaPath, "-class", "Test.Item", "-json", "{"}, code: 1, want: "json parse failed"},
		{name: "missing payload", args: []string{"-schema", schemaPath, "-class", "Test.Item"}, code: 1, want: "Usage of structtool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if code := run(tt.args, &output); code != tt.code {
				t.Fatalf("exit code: want %d, got %d; output: %s", tt.code, code, output.String())
			}
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("output %q does not contain %q", output.String(), tt.want)
			}
		})
	}
}
