import io
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import aver_common as common
import prep_gsm8k
import prep_rag
import to_replay

DATA = os.path.join(common.HERE, "testdata")
PRICES = common.load_json("prices.json")["prices"]


def converted():
    return to_replay.convert(common.read_jsonl(os.path.join(DATA, "prepped.jsonl")), PRICES)


class FieldMapping(unittest.TestCase):
    def test_five_rows_map_field_by_field(self):
        rows = {(r["task"], r["head"]): r for r in converted()}
        self.assertEqual(len(rows), 5)
        gpt4 = rows[("humaneval/12", "gpt-4")]
        self.assertEqual(gpt4["kind"], "code")
        self.assertIs(gpt4["correct"], True)
        self.assertEqual(gpt4["answer_key"], "sample-a")
        self.assertEqual(gpt4["verdicts"], {"base_tests": True, "compiles": True})
        self.assertEqual(gpt4["check_cost_usd"], {"base_tests": 0, "compiles": 0})
        self.assertAlmostEqual(gpt4["cost_usd"], (120 * 30 + 80 * 60) / 1e6, places=10)

    def test_check_that_passed_a_wrong_answer_stays_true(self):
        wrong = [r for r in converted() if r["head"] == "code-llama-7b"][0]
        self.assertIs(wrong["correct"], False)
        self.assertIs(wrong["verdicts"]["base_tests"], True)

    def test_output_is_the_committed_replay_fixture(self):
        want = common.read_jsonl(os.path.join(DATA, "replay.jsonl"))
        self.assertEqual(converted(), want)

    def test_replay_rows_have_exactly_the_fields_replay_read_accepts(self):
        keys = {"task", "kind", "head", "cost_usd", "correct", "answer_key", "verdicts", "check_cost_usd"}
        for r in converted():
            self.assertEqual(set(r), keys)


class Refusals(unittest.TestCase):
    def rows(self):
        return common.read_jsonl(os.path.join(DATA, "prepped.jsonl"))

    def test_unpriced_model_is_refused(self):
        rows = self.rows()
        rows[0]["model"] = "no-such-model"
        with self.assertRaisesRegex(ValueError, "no price"):
            to_replay.convert(rows, PRICES)

    def test_repeated_task_and_head_is_refused(self):
        rows = self.rows()
        with self.assertRaisesRegex(ValueError, "second answer"):
            to_replay.convert(rows + rows[:1], PRICES)

    def test_task_under_two_kinds_is_refused(self):
        rows = self.rows()
        rows[1]["kind"] = "math"
        with self.assertRaisesRegex(ValueError, "two kinds"):
            to_replay.convert(rows, PRICES)

    def test_missing_field_is_refused(self):
        rows = self.rows()
        del rows[0]["answer_key"]
        with self.assertRaisesRegex(ValueError, "missing answer_key"):
            to_replay.convert(rows, PRICES)


class Prices(unittest.TestCase):
    def test_every_price_names_a_dated_snapshot(self):
        d = common.load_json("prices.json")
        for head, p in d["prices"].items():
            self.assertIn(p["snapshot"], d["snapshots"], head)
            self.assertGreater(p["in_per_m"], 0, head)

    def test_every_pinned_model_has_a_price(self):
        sources = common.load_json("sources.json")
        heads = {s["head"] for k in ("humaneval", "mbpp") for s in sources[k]["samples"]}
        heads |= {m["head"] for m in sources["gsm8k"]["models"]}
        self.assertEqual(sorted(heads - set(PRICES)), [])


class GsmChecks(unittest.TestCase):
    def test_final_answer_takes_the_last_marker_and_drops_commas(self):
        self.assertEqual(format(prep_gsm8k.final_answer("x #### 5\n#### 1,250"), "f"), "1250")
        self.assertIsNone(prep_gsm8k.final_answer("the answer is 5"))

    def test_arithmetic_needs_notes_and_every_note_right(self):
        self.assertTrue(prep_gsm8k.arithmetic_ok("<<2*3=6>> then <<6+4=10>> #### 10"))
        self.assertFalse(prep_gsm8k.arithmetic_ok("<<2*3=7>> #### 7"))
        self.assertFalse(prep_gsm8k.arithmetic_ok("no notes #### 7"))

    def test_arithmetic_refuses_what_is_not_plain_arithmetic(self):
        self.assertFalse(prep_gsm8k.arithmetic_ok("<<__import__('os')=1>> #### 1"))


class FakeTable:
    def __init__(self, rows):
        self.rows = rows

    def to_pylist(self):
        return self.rows


class GsmRows(unittest.TestCase):
    def row(self, text, **extra):
        return {"example": "q?", "full_prompt": "Question: q?", "predictions": [text], **extra}

    def test_answer_key_is_a_plain_number_and_rescored_against_gold(self):
        gold = {"q?": (3, prep_gsm8k.number("1250"))}
        [r] = prep_gsm8k.rows_for("m", FakeTable([self.row("<<5*250=1250>> #### 1,250")]), gold)
        self.assertEqual((r["task"], r["answer_key"], r["correct"]), ("gsm8k/0003", "1250", True))
        self.assertEqual(r["tokens_basis"], "estimated")

    def test_leaderboard_token_counts_are_used_when_present(self):
        gold = {"q?": (0, prep_gsm8k.number("2"))}
        row = self.row("#### 2", input_tokens=[[1, 2, 3]], cont_tokens=[[4, 5]])
        [r] = prep_gsm8k.rows_for("m", FakeTable([row]), gold)
        self.assertEqual((r["tokens_in"], r["tokens_out"], r["tokens_basis"]), (3, 2, "exact"))

    def test_unmatched_question_is_dropped(self):
        self.assertEqual(prep_gsm8k.rows_for("m", FakeTable([self.row("#### 1")]), {}), [])


class RagLabels(unittest.TestCase):
    def test_any_labelled_span_means_hallucinated(self):
        self.assertFalse(prep_rag.hallucinated([]))
        self.assertTrue(prep_rag.hallucinated([{"start": 0, "end": 4}]))

    def test_numbers_compare_without_commas_or_trailing_dots(self):
        self.assertEqual(prep_rag.numbers("It cost 1,200. And 3.5 more"), {"1200", "3.5"})


if __name__ == "__main__":
    unittest.main()
