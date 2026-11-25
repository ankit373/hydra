"""Turn prepped answers into the replay rows that `hyctl bench replay` reads.

  python -I to_replay.py --in code.prepped.jsonl --in gsm8k.prepped.jsonl --out replay.jsonl

Cost is tokens times the archived list price in prices.json. A model with no
price is refused, since a made-up cost would change every cost figure.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import aver_common as common

REQUIRED = ("task", "kind", "model", "correct", "answer_key", "checks", "tokens_in", "tokens_out")


def cost_usd(row, price):
    usd = row["tokens_in"] * price["in_per_m"] + row["tokens_out"] * price["out_per_m"]
    return round(usd / 1e6, 10)


def convert(rows, prices):
    out, seen, kinds = [], set(), {}
    for n, row in enumerate(rows, 1):
        missing = [k for k in REQUIRED if k not in row]
        if missing:
            raise ValueError(f"row {n}: missing {', '.join(missing)}")
        task, head = row["task"], row["model"]
        if head not in prices:
            raise ValueError(f"row {n}: no price for model {head!r}; add it to prices.json")
        if (task, head) in seen:
            raise ValueError(f"row {n}: task {task!r} has a second answer from {head!r}")
        if kinds.setdefault(task, row["kind"]) != row["kind"]:
            raise ValueError(f"row {n}: task {task!r} is under two kinds")
        seen.add((task, head))
        out.append({
            "task": task,
            "kind": row["kind"],
            "head": head,
            "cost_usd": cost_usd(row, prices[head]),
            "correct": bool(row["correct"]),
            "answer_key": row["answer_key"],
            "verdicts": {name: bool(v) for name, v in row["checks"].items()},
            "check_cost_usd": {name: 0 for name in row["checks"]},
        })
    return sorted(out, key=lambda r: (r["task"], r["head"]))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    ap.add_argument("--in", dest="inputs", action="append", required=True, metavar="FILE")
    ap.add_argument("--out", required=True)
    ap.add_argument("--prices", default=os.path.join(common.HERE, "prices.json"))
    a = ap.parse_args()
    with open(a.prices, encoding="utf-8") as f:
        prices = json.load(f)["prices"]
    rows = [r for path in a.inputs for r in common.read_jsonl(path)]
    try:
        out = convert(rows, prices)
    except ValueError as e:
        sys.exit(f"to_replay: {e}")
    common.write_jsonl(a.out, out)
    print(f"wrote {len(out)} rows to {a.out}")


if __name__ == "__main__":
    main()
