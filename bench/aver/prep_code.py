"""HumanEval+ and MBPP+: fetch EvalPlus's published greedy samples, then label them.

  python -I prep_code.py fetch --data DIR
  bash run_evalplus.sh DIR
  python -I prep_code.py merge --data DIR --out DIR/code.prepped.jsonl
"""
import argparse
import ast
import gzip
import hashlib
import json
import os
import re
import sys
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import aver_common as common

SUITES = ("humaneval", "mbpp")
SAMPLE = re.compile(r"^[^/]+/((?:HumanEval|Mbpp)_\d+)/0\.py$")


def fetch(data):
    sources = common.load_json("sources.json")
    for suite in SUITES:
        spec = sources[suite]
        common.fetch(spec["problems"]["url"], spec["problems"]["sha256"],
                     os.path.join(data, "downloads", suite, "problems.jsonl.gz"))
        for s in spec["samples"]:
            zpath = common.fetch(s["url"], s["sha256"],
                                 os.path.join(data, "downloads", suite, s["dir"] + ".zip"))
            extract(zpath, os.path.join(data, "samples", suite, s["head"]))


def extract(zpath, dest):
    """Keep only <task>/0.py. Member names are matched, never joined blindly."""
    with zipfile.ZipFile(zpath) as z:
        for info in z.infolist():
            m = SAMPLE.match(info.filename)
            if not m:
                continue
            target = os.path.join(dest, m.group(1), "0.py")
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "wb") as f:
                f.write(z.read(info))


def problems(data, suite):
    path = os.path.join(data, "downloads", suite, "problems.jsonl.gz")
    with gzip.open(path, "rt", encoding="utf-8") as f:
        return {p["task_id"]: p for p in map(json.loads, f)}


def code_key(code):
    return hashlib.sha256(" ".join(code.split()).encode()).hexdigest()[:16]


def compiles(code):
    try:
        ast.parse(code)
        return True
    except (SyntaxError, ValueError):
        return False


def merge(data, out):
    sources = common.load_json("sources.json")
    rows, skipped = [], []
    for suite in SUITES:
        probs = problems(data, suite)
        for s in sources[suite]["samples"]:
            head = s["head"]
            rpath = os.path.join(data, "results", suite, head + ".json")
            if not os.path.exists(rpath):
                skipped.append(f"{suite}/{head}")
                continue
            with open(rpath, encoding="utf-8") as f:
                evals = json.load(f)["eval"]
            slow = sum(1 for e in evals.values() if e and "timeout" in (e[0]["base_status"], e[0]["plus_status"]))
            if slow:
                print(f"{suite}/{head}: {slow} tasks timed out; rerun on an idle machine if that looks high",
                      file=sys.stderr)
            for task_id, entries in sorted(evals.items()):
                if not entries or task_id not in probs:
                    continue
                e = entries[0]
                code = e["solution"]
                base = e["base_status"] == "pass"
                plus = e["plus_status"] == "pass"
                rows.append({
                    "suite": suite,
                    "task": f"{suite}/{task_id.split('/')[1]}",
                    "kind": "code",
                    "model": head,
                    "correct": base and plus,
                    "answer_key": code_key(code),
                    "checks": {"base_tests": base, "compiles": compiles(code)},
                    "tokens_in": common.estimate_tokens(probs[task_id]["prompt"]),
                    "tokens_out": common.estimate_tokens(code),
                    "tokens_basis": "estimated",
                })
    if skipped:
        print(f"no evalplus result yet for: {', '.join(skipped)}", file=sys.stderr)
    common.write_jsonl(out, rows)
    print(f"wrote {len(rows)} rows to {out}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch")
    f.add_argument("--data", required=True)
    m = sub.add_parser("merge")
    m.add_argument("--data", required=True)
    m.add_argument("--out", required=True)
    a = ap.parse_args()
    fetch(a.data) if a.cmd == "fetch" else merge(a.data, a.out)


if __name__ == "__main__":
    main()
