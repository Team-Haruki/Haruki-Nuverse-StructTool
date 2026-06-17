// go example: read Unity MsgpackSchemaExporter Avro schemas and decode/encode
// compact msgpack without needing the C# CLI at runtime.

package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	AvroParser "github.com/Team-Haruki/Haruki-Nuverse-StructTool/avro_parser"
)

func main() {
	schemaFile := flag.String("schema", "", "Avro schema JSON file (single or --all output)")
	className := flag.String("class", "", "Class name to use as root schema")
	hexData := flag.String("hex", "", "Compact msgpack bytes as hex string to decode")
	jsonData := flag.String("json", "", "JSON string to encode to compact msgpack")
	verbose := flag.Bool("v", false, "Enable verbose (debug) logging")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: logLevel}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))

	if *schemaFile == "" || *className == "" {
		flag.Usage()
		os.Exit(1)
	}

	reg, _, err := AvroParser.LoadFile(*schemaFile)
	if err != nil {
		logger.Error("failed to load schema", "error", err)
		os.Exit(1)
	}

	schema := reg[*className]
	if schema == nil {
		logger.Error("class not found in schema", "class", *className, "available", schemaNames(reg))
		os.Exit(1)
	}

	switch {
	case *hexData != "":
		data, err := hex.DecodeString(*hexData)
		if err != nil {
			logger.Error("hex decode failed", "error", err)
			os.Exit(1)
		}

		value, err := AvroParser.Decode(schema, data)
		if err != nil {
			logger.Error("decode failed", "error", err)
			os.Exit(1)
		}

		out, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(out))

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
			os.Exit(1)
		}

		data, err := AvroParser.Encode(schema, obj)
		if err != nil {
			logger.Error("encode failed", "error", err)
			os.Exit(1)
		}

		hexOutput := strings.ToUpper(hex.EncodeToString(data))
		fmt.Println(hexOutput)

		value, err := AvroParser.Decode(schema, data)
		if err != nil {
			logger.Debug("round-trip decode failed", "error", err)
		} else {
			out, _ := json.MarshalIndent(value, "", "  ")
			logger.Debug("decoded back", "json", string(out))
		}

	default:
		flag.Usage()
		os.Exit(1)
	}
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
