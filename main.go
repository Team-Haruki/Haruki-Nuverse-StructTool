// go example: read Unity MsgpackSchemaExporter Avro schemas and decode/encode
// compact msgpack without needing the C# CLI at runtime.

package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	AvroParser "github.com/Team-Haruki/Haruki-Nuverse-StructTool/avro_parser"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("structtool", flag.ContinueOnError)
	flags.SetOutput(stdout)
	schemaFile := flags.String("schema", "", "Avro schema JSON file (single or --all output)")
	className := flags.String("class", "", "Class name to use as root schema")
	hexData := flags.String("hex", "", "Compact msgpack bytes as hex string to decode")
	jsonData := flags.String("json", "", "JSON string to encode to compact msgpack")
	verbose := flags.Bool("v", false, "Enable verbose (debug) logging")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: logLevel}
	logger := slog.New(slog.NewTextHandler(stdout, opts))

	if *schemaFile == "" || *className == "" {
		flags.Usage()
		return 1
	}

	reg, _, err := AvroParser.LoadFile(*schemaFile)
	if err != nil {
		logger.Error("failed to load schema", "error", err)
		return 1
	}

	schema := reg[*className]
	if schema == nil {
		logger.Error("class not found in schema", "class", *className, "available", schemaNames(reg))
		return 1
	}

	switch {
	case *hexData != "":
		data, err := hex.DecodeString(*hexData)
		if err != nil {
			logger.Error("hex decode failed", "error", err)
			return 1
		}

		value, err := AvroParser.Decode(schema, data)
		if err != nil {
			logger.Error("decode failed", "error", err)
			return 1
		}

		out, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(stdout, string(out))

		reEncoded, err := AvroParser.Encode(schema, value)
		if err != nil {
			logger.Error("re-encode failed", "error", err)
		} else {
			reHex := hex.EncodeToString(reEncoded)
			match := strings.EqualFold(reHex, *hexData)

			logger.Debug("round-trip verification",
				"re_encoded", reHex,
				"match", match,
			)
		}

	case *jsonData != "":
		var obj any
		if err := json.Unmarshal([]byte(*jsonData), &obj); err != nil {
			logger.Error("json parse failed", "error", err)
			return 1
		}

		data, err := AvroParser.Encode(schema, obj)
		if err != nil {
			logger.Error("encode failed", "error", err)
			return 1
		}

		hexOutput := strings.ToUpper(hex.EncodeToString(data))
		fmt.Fprintln(stdout, hexOutput)

		value, err := AvroParser.Decode(schema, data)
		if err != nil {
			logger.Debug("round-trip decode failed", "error", err)
		} else {
			out, _ := json.MarshalIndent(value, "", "  ")
			logger.Debug("decoded back", "json", string(out))
		}

	default:
		flags.Usage()
		return 1
	}
	return 0
}

func schemaNames(reg AvroParser.Registry) []string {
	var names []string
	for k, v := range reg {
		if v.Type == "record" && k == v.Name {
			names = append(names, k)
		}
	}
	return names
}
