// validate loads a Dune export CSV, scores every token, and prints the
// backtest Summary as JSON to stdout. Use this to verify that Go scoring
// output matches Dune query output within rounding tolerance.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"memecoin_scorer/internal/backtest"
	"memecoin_scorer/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: validate <csv-path> [config-path]")
		os.Exit(1)
	}

	csvPath := os.Args[1]
	cfgPath := "config/scoring_config.yaml"
	if len(os.Args) >= 3 {
		cfgPath = os.Args[2]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("loading config %s: %v", cfgPath, err)
	}

	results, summary, err := backtest.Run(csvPath, cfg)
	if err != nil {
		log.Fatalf("running backtest on %s: %v", csvPath, err)
	}

	// Per-token quick view.
	fmt.Fprintf(os.Stderr, "Scored %d tokens\n", len(results))
	for _, r := range results {
		fmt.Fprintf(os.Stderr, "  %-44s  tradeable=%-5v  clean=%-5v  score=%.1f\n",
			r.TokenMint,
			r.Score.IsTradeable30m,
			r.Score.IsCleanTradeable30m,
			r.Score.OpportunityScore,
		)
	}

	// Summary JSON to stdout for diffing against Dune output.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summary); err != nil {
		log.Fatalf("encoding summary: %v", err)
	}
}
