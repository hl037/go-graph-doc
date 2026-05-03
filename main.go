package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/hl037/go-graph-doc/internal/analysis"
	"github.com/hl037/go-graph-doc/internal/server"
)

func main() {
	dir := flag.String("dir", "", "project directory to analyze (default: current directory)")
	focus := flag.String("focus", "", "focus: only include packages whose path contains this string")
	noStd := flag.Bool("nostd", true, "exclude standard library packages")
	noVendor := flag.Bool("novendor", true, "exclude vendor packages")
	tests := flag.Bool("tests", false, "include test functions")
	output := flag.String("o", "", "output file path (use - for stdout); requires -json or -html")
	outJSON := flag.Bool("json", false, "output raw JSON")
	outHTML := flag.Bool("html", false, "output standalone HTML")
	addr := flag.String("addr", ":8080", "HTTP server listen address")

	defaultDataDir := filepath.Join(xdg.DataHome, "go-graph-doc")
	dataDir := flag.String("datadir", defaultDataDir, "directory for per-user saved state (e.g. ~/.config/go-graph-doc)")
	userHeader := flag.String("user-header", "", "HTTP header name used to identify the user; disables save/load endpoints if empty")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	cfg := analysis.Config{
		Patterns:      patterns,
		Dir:           *dir,
		Focus:         *focus,
		ExcludeStd:    *noStd,
		ExcludeVendor: *noVendor,
		Tests:         *tests,
	}

	fmt.Fprintf(os.Stderr, "analyzing %v...\n", patterns)
	graphData, err := analysis.Analyze(cfg)
	if err != nil {
		log.Fatalf("analysis error: %v", err)
	}
	fmt.Fprintf(os.Stderr, "found %d packages, %d edges\n", len(graphData.Packages), len(graphData.Edges))

	// Determine output mode
	if *outJSON || *outHTML || *output != "" {
		var out *os.File
		if *output == "" || *output == "-" {
			out = os.Stdout
		} else {
			out, err = os.Create(*output)
			if err != nil {
				log.Fatalf("creating output file: %v", err)
			}
			defer out.Close()
		}

		if *outHTML {
			if err := server.RenderHTML(out, graphData); err != nil {
				log.Fatalf("rendering HTML: %v", err)
			}
		} else {
			// default to JSON when -o is used without format flag
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(graphData); err != nil {
				log.Fatalf("encoding JSON: %v", err)
			}
		}
		return
	}

	// Server mode
	srv := server.New(*addr, graphData, *dataDir, *userHeader)
	fmt.Printf("serving at http://localhost%s\n", *addr)
	if *userHeader != "" {
		fmt.Printf("user state dir: %s\n", *dataDir)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
