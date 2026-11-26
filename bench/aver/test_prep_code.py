import gzip
import importlib.util
import json
import os
import sys
import tempfile
import unittest
import zipfile
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import prep_code

CHAT_REPLY = """Here is the Python function:

```python
def add(a, b):
    return a + b

print(add(1, 2))
```

It adds the two numbers."""

TASKS = ("HumanEval/0", "HumanEval/1")
WANT = {"HumanEval_0", "HumanEval_1"}


class Tmp(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = self._tmp.name
        self.addCleanup(self._tmp.cleanup)

    def zip(self, members):
        path = os.path.join(self.dir, f"z{len(os.listdir(self.dir))}.zip")
        with zipfile.ZipFile(path, "w") as z:
            for name, body in members.items():
                z.writestr(name, body)
        return path

    def write_problems(self, suite, task_ids):
        path = os.path.join(self.dir, "downloads", suite, "problems.jsonl.gz")
        os.makedirs(os.path.dirname(path))
        with gzip.open(path, "wt", encoding="utf-8") as f:
            for t in task_ids:
                f.write(json.dumps({"task_id": t, "prompt": "p", "entry_point": "add"}) + "\n")

    def read(self, *parts):
        with open(os.path.join(self.dir, *parts), encoding="utf-8") as f:
            return f.read()


class Extract(Tmp):
    def extracted(self, zpath):
        dest = os.path.join(self.dir, "out")
        n = prep_code.extract(zpath, dest, WANT)
        return n, sorted(os.listdir(dest))

    def test_flat_layout(self):
        z = self.zip({"gpt-4_temp_0.0/HumanEval_0/0.py": "a", "gpt-4_temp_0.0/HumanEval_1/0.py": "b"})
        self.assertEqual(self.extracted(z), (2, ["HumanEval_0", "HumanEval_1"]))
        self.assertEqual(self.read("out", "HumanEval_1", "0.py"), "b")

    def test_nested_layout(self):
        top = "JawTitan/EvalPlus/humaneval/code-llama-7b_temp_0.0/"
        z = self.zip({top + "HumanEval_0/0.py": "a", top + "HumanEval_1/0.py": "b", "README.txt": "x"})
        self.assertEqual(self.extracted(z), (2, ["HumanEval_0", "HumanEval_1"]))

    def test_task_at_two_depths_is_refused(self):
        z = self.zip({"a/HumanEval_0/0.py": "a", "a/b/HumanEval_0/0.py": "b", "a/HumanEval_1/0.py": "c"})
        with self.assertRaisesRegex(ValueError, "HumanEval_0 is in the zip twice"):
            prep_code.extract(z, os.path.join(self.dir, "out"), WANT)

    def test_missing_problem_is_refused_and_nothing_written(self):
        z = self.zip({"a/HumanEval_0/0.py": "a", "a/HumanEval_1/1.py": "not sample 0"})
        dest = os.path.join(self.dir, "out")
        with self.assertRaisesRegex(ValueError, "1 of 2 problems have a sample; missing HumanEval_1"):
            prep_code.extract(z, dest, WANT)
        self.assertFalse(os.path.exists(dest))


class Sanitize(Tmp):
    def sample(self, task, body):
        path = os.path.join(self.dir, "samples", "humaneval", "m", task, "0.py")
        os.makedirs(os.path.dirname(path))
        with open(path, "w", encoding="utf-8") as f:
            f.write(body)

    def test_every_problem_is_cleaned_with_its_entry_point(self):
        self.write_problems("humaneval", TASKS)
        self.sample("HumanEval_0", "a")
        self.sample("HumanEval_1", "b")
        seen = []
        prep_code.sanitize(self.dir, "humaneval", "m", lambda code, entry: seen.append(entry) or code.upper())
        self.assertEqual(seen, ["add", "add"])
        self.assertEqual(self.read("sanitized", "humaneval", "m", "HumanEval_1", "0.py"), "B")

    @unittest.skipUnless(importlib.util.find_spec("evalplus"), "needs evalplus==0.3.1")
    def test_evalplus_strips_a_chat_reply_to_the_function(self):
        from evalplus.sanitize import sanitize
        self.assertFalse(prep_code.compiles(CHAT_REPLY))
        got = sanitize(CHAT_REPLY, "add")
        self.assertEqual(got, "def add(a, b):\n    return a + b")
        self.assertTrue(prep_code.compiles(got))


class Merge(Tmp):
    def scored(self, head, solutions):
        clean = {t: "def add(a, b):\n    return a + b" for t in TASKS}
        for t, code in clean.items():
            path = os.path.join(self.dir, "sanitized", "humaneval", head, prep_code.task_dir(t), "0.py")
            os.makedirs(os.path.dirname(path))
            with open(path, "w", encoding="utf-8") as f:
                f.write(code)
        evals = {t: [{"solution": solutions.get(t, clean[t]), "base_status": "pass", "plus_status": "pass"}]
                 for t in TASKS if t not in solutions or solutions[t] is not None}
        path = os.path.join(self.dir, "results", "humaneval", head + ".json")
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            json.dump({"eval": evals}, f)

    def merge(self):
        self.write_problems("humaneval", TASKS)
        self.write_problems("mbpp", [])
        sources = {"humaneval": {"samples": [{"head": "m"}]}, "mbpp": {"samples": []}}
        with mock.patch.object(prep_code.common, "load_json", return_value=sources):
            prep_code.merge(self.dir, os.path.join(self.dir, "code.prepped.jsonl"))

    def test_full_result_is_written(self):
        self.scored("m", {})
        self.merge()
        rows = prep_code.common.read_jsonl(os.path.join(self.dir, "code.prepped.jsonl"))
        self.assertEqual([r["task"] for r in rows], ["humaneval/0", "humaneval/1"])
        self.assertTrue(all(r["checks"]["compiles"] for r in rows))

    def test_missing_result_fails(self):
        with self.assertRaisesRegex(SystemExit, "no evalplus result for: humaneval/m"):
            self.merge()

    def test_short_result_fails(self):
        self.scored("m", {"HumanEval/1": None})
        with self.assertRaisesRegex(SystemExit, "humaneval/m: 1 of 2 problems scored"):
            self.merge()

    def test_result_of_unsanitized_sample_fails(self):
        self.scored("m", {"HumanEval/0": CHAT_REPLY})
        with self.assertRaisesRegex(SystemExit, "did not score the sanitized sample for HumanEval/0"):
            self.merge()


if __name__ == "__main__":
    unittest.main()
