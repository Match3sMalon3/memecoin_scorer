#!/usr/bin/env python3
import json
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: liquidity_proxy_compare.py /path/to/live.json", file=sys.stderr)
        return 2

    with open(sys.argv[1]) as f:
        rows = json.load(f)

    print("mint,liq,source,reliable,band,score,risks")
    for row in rows:
        ep = row.get("early_proxy", {})
        risks = "|".join(ep.get("risk_flags") or [])
        print(
            ",".join(
                [
                    str(row.get("mint", "")),
                    f"{float(row.get('liquidity_proxy_sol') or 0):.2f}",
                    str(row.get("liquidity_evidence_source", "")),
                    str(row.get("liquidity_proxy_reliable", "")),
                    str(ep.get("band", "")),
                    f"{float(ep.get('score') or 0):.2f}",
                    risks,
                ]
            )
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
