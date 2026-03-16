package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aptos-labs/jc-contract-integration/internal/api/openapi"
)

// main runs an OpenAPI server for viewing the OpenAPI document
func main() {
	outFile := flag.String("o", "", "output file path (default: stdout)")
	format := flag.String("format", "yaml", "output format: yaml or json")
	flag.Parse()

	spec := openapi.Spec()

	var (
		data []byte
		err  error
	)
	switch *format {
	case "yaml":
		data, err = spec.MarshalYAML()
	case "json":
		data, err = json.MarshalIndent(spec, "", "  ")
	default:
		_, err := fmt.Fprintf(os.Stderr, "unknown format: %s (use yaml or json)\n", *format)
		if err != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "marshal error: %v\n", err)
		if err != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, data, 0o644); err != nil {
			_, err := fmt.Fprintf(os.Stderr, "write error: %v\n", err)
			if err != nil {
				os.Exit(2)
			}
			os.Exit(1)
		}
		_, err := fmt.Fprintf(os.Stderr, "wrote %s\n", *outFile)
		if err != nil {
			os.Exit(2)
		}
		return
	}

	_, _ = os.Stdout.Write(data)
}
