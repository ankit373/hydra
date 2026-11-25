"""RAGTruth: label the dataset's own answers. These are never a live suite.

  python -I prep_rag.py --data DIR --out DIR/ragtruth.prepped.jsonl

An answer counts as correct when the annotators marked no hallucinated span in it.
"""
import argparse
import hashlib
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import aver_common as common

NUMBER = re.compile(r"\d[\d,]*\.?\d*")


def numbers(text):
    return {n.rstrip(".,").replace(",", "") for n in NUMBER.findall(text)}


def answer_key(text):
    return hashlib.sha256(" ".join(text.lower().split()).encode()).hexdigest()[:16]


def hallucinated(labels):
    return bool(labels) if not isinstance(labels, str) else labels.strip() not in ("", "[]")


def build(responses, sources):
    by_id = {s["source_id"]: s for s in sources}
    rows = []
    for r in responses:
        src = by_id[r["source_id"]]
        text = r["response"]
        rows.append({
            "suite": "ragtruth",
            "task": f"ragtruth/{r['source_id']}",
            "kind": "grounded",
            "model": r["model"],
            "correct": not hallucinated(r["labels"]),
            "answer_key": answer_key(text),
            "checks": {
                "numbers_in_source": numbers(text) <= numbers(src["prompt"]),
                "complete": r["quality"] == "good",
            },
            "tokens_in": common.estimate_tokens(src["prompt"]),
            "tokens_out": common.estimate_tokens(text),
            "tokens_basis": "estimated",
        })
    return rows


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    spec = common.load_json("sources.json")["ragtruth"]
    paths = {
        k: common.fetch(spec[k]["url"], spec[k]["sha256"], os.path.join(a.data, "downloads", "ragtruth", k + ".jsonl"))
        for k in ("responses", "sources")
    }
    rows = build(common.read_jsonl(paths["responses"]), common.read_jsonl(paths["sources"]))
    common.write_jsonl(a.out, rows)
    print(f"wrote {len(rows)} rows to {a.out}")


if __name__ == "__main__":
    main()
