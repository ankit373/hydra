"""HumanEval+ and MBPP+: fetch EvalPlus's published greedy samples, then label them.

  python -I prep_code.py fetch --data DIR
  bash run_evalplus.sh DIR    (runs evalplus.sanitize, then evalplus.evaluate)
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
# Zips nest the task folder at different depths, so only the tail is matched.
SAMPLE = re.compile(r"^(?:[^/]+/)*((?:HumanEval|Mbpp)_\d+)/0\.py$")


def fetch(data):
    sources = common.load_json("sources.json")
    for suite in SUITES:
        spec = sources[suite]
        common.fetch(spec["problems"]["url"], spec["problems"]["sha256"],
                     os.path.join(data, "downloads", suite, "problems.jsonl.gz"))
        want = {task_dir(t) for t in problems(data, suite)}
        for s in spec["samples"]:
            zpath = common.fetch(s["url"], s["sha256"],
                                 os.path.join(data, "downloads", suite, s["dir"] + ".zip"))
            extract(zpath, os.path.join(data, "samples", suite, s["head"]), want)


def task_dir(task_id):
    return task_id.replace("/", "_")


def extract(zpath, dest, want):
    """Keep only <task>/0.py. Member names are matched, never joined blindly.

    Refuses a zip that has a task twice or lacks a sample for any task in want.
    """
    found = {}
    with zipfile.ZipFile(zpath) as z:
        for info in z.infolist():
            m = SAMPLE.match(info.filename)
            if not m:
                continue
            task = m.group(1)
            if task in found:
                raise ValueError(f"{zpath}: {task} is in the zip twice: {found[task].filename}, {info.filename}")
            found[task] = info
        missing = sorted(want - found.keys())
        if missing:
            raise ValueError(f"{zpath}: {len(want) - len(missing)} of {len(want)} problems have a sample; "
                             f"missing {', '.join(missing[:5])}")
        for task, info in sorted(found.items()):
            target = os.path.join(dest, task, "0.py")
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "wb") as f:
                f.write(z.read(info))
    return len(found)


def sanitize(data, suite, head, clean):
    """Write clean(sample, entry_point) for every problem, which is what evalplus.sanitize does per file."""
    src = os.path.join(data, "samples", suite, head)
    dst = os.path.join(data, "sanitized", suite, head)
    for task_id, p in sorted(problems(data, suite).items()):
        name = task_dir(task_id)
        with open(os.path.join(src, name, "0.py"), encoding="utf-8") as f:
            code = clean(f.read(), p["entry_point"])
        os.makedirs(os.path.join(dst, name), exist_ok=True)
        with open(os.path.join(dst, name, "0.py"), "w", encoding="utf-8") as f:
            f.write(code)


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


def scored_sample(data, suite, head, task_id):
    path = os.path.join(data, "sanitized", suite, head, task_dir(task_id), "0.py")
    with open(path, encoding="utf-8") as f:
        return f.read()


def merge(data, out):
    sources = common.load_json("sources.json")
    rows, missing = [], []
    for suite in SUITES:
        probs = problems(data, suite)
        for s in sources[suite]["samples"]:
            head = s["head"]
            rpath = os.path.join(data, "results", suite, head + ".json")
            if not os.path.exists(rpath):
                missing.append(f"{suite}/{head}")
                continue
            with open(rpath, encoding="utf-8") as f:
                evals = json.load(f)["eval"]
            slow = sum(1 for e in evals.values() if e and "timeout" in (e[0]["base_status"], e[0]["plus_status"]))
            if slow:
                print(f"{suite}/{head}: {slow} tasks timed out; rerun on an idle machine if that looks high",
                      file=sys.stderr)
            before = len(rows)
            for task_id, entries in sorted(evals.items()):
                if not entries or task_id not in probs:
                    continue
                e = entries[0]
                code = e["solution"]
                if code != scored_sample(data, suite, head, task_id):
                    sys.exit(f"{rpath} did not score the sanitized sample for {task_id}; delete it and rescore")
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
            if len(rows) - before != len(probs):
                sys.exit(f"{suite}/{head}: {len(rows) - before} of {len(probs)} problems scored")
    if missing:
        sys.exit(f"no evalplus result for: {', '.join(missing)}")
    common.write_jsonl(out, rows)
    print(f"wrote {len(rows)} rows to {out}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch")
    f.add_argument("--data", required=True)
    s = sub.add_parser("sanitize", help="needs evalplus, so run it with EvalPlus's venv")
    s.add_argument("--data", required=True)
    s.add_argument("--suite", required=True, choices=SUITES)
    s.add_argument("--head", required=True)
    m = sub.add_parser("merge")
    m.add_argument("--data", required=True)
    m.add_argument("--out", required=True)
    a = ap.parse_args()
    if a.cmd == "fetch":
        fetch(a.data)
    elif a.cmd == "sanitize":
        from evalplus.sanitize import sanitize as clean
        sanitize(a.data, a.suite, a.head, clean)
    else:
        merge(a.data, a.out)


if __name__ == "__main__":
    main()
