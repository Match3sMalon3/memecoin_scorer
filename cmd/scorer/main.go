package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"memecoin_scorer/internal/backtest"
	"memecoin_scorer/internal/config"
	"memecoin_scorer/internal/report"
)

func main() {
	configPath := flag.String("config", "config/scoring_config.yaml", "path to scoring config YAML")
	inputCSV := flag.String("input", "", "path to input CSV (required)")
	outputCSV := flag.String("output-csv", "", "path for per-token results CSV (default: stdout)")
	outputJSON := flag.String("output-json", "", "path for summary JSON (default: stderr)")
	flag.Parse()

	if *inputCSV == "" {
		fmt.Fprintln(os.Stderr, "error: -input is required")
		flag.Usage()
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	results, summary, err := backtest.Run(*inputCSV, cfg)
	if err != nil {
		log.Fatalf("backtest: %v", err)
	}

	// Per-token CSV output.
	csvOut := os.Stdout
	if *outputCSV != "" {
		f, err := os.Create(*outputCSV)
		if err != nil {
			log.Fatalf("creating CSV output: %v", err)
		}
		defer f.Close()
		csvOut = f
	}
	if err := report.WriteCSV(csvOut, results); err != nil {
		log.Fatalf("writing CSV: %v", err)
	}

	// Summary JSON output.
	jsonOut := os.Stderr
	if *outputJSON != "" {
		f, err := os.Create(*outputJSON)
		if err != nil {
			log.Fatalf("creating JSON output: %v", err)
		}
		defer f.Close()
		jsonOut = f
	}
	if err := report.WriteJSON(jsonOut, summary); err != nil {
		log.Fatalf("writing JSON: %v", err)
	}
}
