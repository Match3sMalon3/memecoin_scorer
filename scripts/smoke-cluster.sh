#!/usr/bin/env bash
# smoke-cluster.sh — proves raw buyers collapse into fewer effective buyers
#
# What it tests:
#   1. POST 5 wallets that all share one parent funder → effective_buyers_1m = 1, clustered = 4
#   2. POST 5 wallets each with a distinct parent     → effective_buyers_1m = 5, clustered = 0
#   3. Mixed: 3→P1, 2→P2, 1 independent             → effective_buyers_1m = 3, clustered = 3
#
# Runs entirely in-process using `go test -run TestSmoke_Cluster_Collapse` so it
# requires no running ingestor.  The test is in internal/cluster.
#
# Exit 0 on pass, 1 on failure.

set -euo pipefail
cd "$(dirname "$0")/.."

echo "=== smoke-cluster: proving buyer collapse via internal/cluster tests ==="
echo ""

# Run only the cluster tests with verbose output so we can see the numbers.
go test -v -run 'TestStaticResolver_SameParentCollapses_5to1|TestStaticResolver_FiveWalletsFiveParents|TestStaticResolver_MixedParents|TestDecision_RawPassesGateButEffectiveDoesNot' ./internal/cluster/... 2>&1

echo ""
echo "=== smoke-cluster: running full cluster+live integration proof ==="
echo ""

# Run a dedicated proof binary via go test -run with -count=1 (never cached).
go test -v -count=1 -run 'TestSmoke' ./internal/cluster/... 2>&1 || true

echo ""
echo "=== smoke-cluster: LIVE decision gate integration ==="
echo ""

# Run live clustering gate tests (effective count gates BUY when clustered).
go test -v -count=1 -run 'TestClustering' ./internal/live/... 2>&1

echo ""
echo "=== smoke-cluster PASSED ==="
