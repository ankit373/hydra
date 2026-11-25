"""GSM8K: re-score the Open LLM Leaderboard v1 per-sample details against the GSM8K gold.

  venv/bin/python -I prep_gsm8k.py --data DIR --out DIR/gsm8k.prepped.jsonl

Needs pyarrow. The detail repos state no licence, so the output stays on this machine.
"""
import argparse
import ast
import operator
import os
import re
import sys
from decimal import Decimal, InvalidOperation

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import aver_common as common

FINAL = re.compile(r"####\s*(-?[\d,]*\.?\d+)")
NOTE = re.compile(r"<<([^<>=]+)=([^<>]+)>>")
OPS = {ast.Add: operator.add, ast.Sub: operator.sub, ast.Mult: operator.mul,
       ast.Div: operator.truediv, ast.USub: operator.neg}


def number(text):
    try:
        return Decimal(text.replace(",", "")).normalize()
    except InvalidOperation:
        return None


def final_answer(text):
    m = FINAL.findall(text)
    return number(m[-1]) if m else None


def evaluate(node):
    if isinstance(node, ast.Expression):
        return evaluate(node.body)
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return Decimal(str(node.value))
    if isinstance(node, ast.BinOp) and type(node.op) in OPS:
        return OPS[type(node.op)](evaluate(node.left), evaluate(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in OPS:
        return OPS[type(node.op)](evaluate(node.operand))
    raise ValueError("not plain arithmetic")


def arithmetic_ok(text):
    """True when the answer has calculator notes and every one is right."""
    notes = NOTE.findall(text)
    if not notes:
        return False
    for expr, result in notes:
        try:
            got = evaluate(ast.parse(expr.strip().replace(",", ""), mode="eval"))
        except (ValueError, SyntaxError, ZeroDivisionError, InvalidOperation):
            return False
        want = number(result)
        if want is None or abs(got - want) > Decimal("0.01"):
            return False
    return True


def gold_answers(path):
    gold = {}
    for i, row in enumerate(common.read_jsonl(path)):
        gold[row["question"]] = (i, number(row["answer"].split("####")[-1].strip()))
    return gold


def first(values, default=None):
    return values[0] if values is not None and len(values) else default


def rows_for(model, table, gold):
    out = []
    for r in table.to_pylist():
        hit = gold.get(r["example"])
        if hit is None:
            continue
        index, want = hit
        text = first(r["predictions"], "")
        got = final_answer(text)
        in_ids, out_ids = first(r.get("input_tokens")), first(r.get("cont_tokens"))
        exact = in_ids is not None and out_ids is not None
        out.append({
            "suite": "gsm8k",
            "task": f"gsm8k/{index:04d}",
            "kind": "math",
            "model": model,
            "correct": got is not None and got == want,
            "answer_key": format(got, "f") if got is not None else f"none:{model}",
            "checks": {"format": got is not None, "arithmetic": arithmetic_ok(text)},
            "tokens_in": len(in_ids) if exact else common.estimate_tokens(r["full_prompt"]),
            "tokens_out": len(out_ids) if exact else common.estimate_tokens(text),
            "tokens_basis": "exact" if exact else "estimated",
        })
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    try:
        import pyarrow.parquet as pq
    except ImportError:
        sys.exit("prep_gsm8k.py needs pyarrow: pip install pyarrow")
    spec = common.load_json("sources.json")["gsm8k"]
    gold = gold_answers(common.fetch(spec["gold"]["url"], spec["gold"]["sha256"],
                                     os.path.join(a.data, "downloads", "gsm8k", "test.jsonl")))
    rows = []
    for m in spec["models"]:
        path = common.fetch(m["url"], m["sha256"],
                            os.path.join(a.data, "downloads", "gsm8k", m["head"] + ".parquet"))
        got = rows_for(m["head"], pq.read_table(path), gold)
        print(f"{m['head']}: {len(got)} of {len(gold)} gold questions matched")
        rows += got
    common.write_jsonl(a.out, rows)
    print(f"wrote {len(rows)} rows to {a.out}")


if __name__ == "__main__":
    main()
