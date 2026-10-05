"""Тесты счёта: `python3 -m unittest discover -s bench/stt` из корня репозитория или `python3 -m unittest` отсюда."""
import os
import re
import unittest

import score

HERE = os.path.dirname(os.path.abspath(__file__))


class Normalize(unittest.TestCase):
    def test_case_punctuation_and_yo(self):
        self.assertEqual(score.normalize("Ребёнок, УСНУЛ!"), "ребенок уснул")

    def test_hyphens_and_dashes_split_words(self):
        self.assertEqual(score.normalize("пулл-реквест — готов"), "пулл реквест готов")

    def test_apostrophes_are_dropped_on_both_sides(self):
        self.assertEqual(score.normalize("I'm sure I’ll go"), "im sure ill go")

    def test_clock_time_becomes_two_tokens(self):
        self.assertEqual(score.normalize("в 15.30"), "в 15 30")
        self.assertEqual(score.normalize("в 15:30"), "в 15 30")

    def test_spacing_is_collapsed(self):
        self.assertEqual(score.normalize("  a \n b\t"), "a b")

    def test_empty(self):
        self.assertEqual(score.tokens(""), [])


class EditDistance(unittest.TestCase):
    def test_equal(self):
        self.assertEqual(score.edit_distance(["a", "b"], ["a", "b"]), 0)

    def test_substitution_insertion_deletion(self):
        self.assertEqual(score.edit_distance(["a", "b", "c"], ["a", "x", "c"]), 1)
        self.assertEqual(score.edit_distance(["a", "c"], ["a", "b", "c"]), 1)
        self.assertEqual(score.edit_distance(["a", "b", "c"], ["a", "c"]), 1)

    def test_against_empty(self):
        self.assertEqual(score.edit_distance([], ["a", "b"]), 2)
        self.assertEqual(score.edit_distance(["a", "b", "c"], []), 3)


class BestVariant(unittest.TestCase):
    def test_exact_match_is_zero_errors(self):
        errors, words, _ = score.best_variant(["Привет, мир"], "привет мир")
        self.assertEqual((errors, words), (0, 2))

    def test_one_error_in_four_words(self):
        errors, words, _ = score.best_variant(["раз два три четыре"], "раз два три пять")
        self.assertEqual((errors, words), (1, 4))

    def test_picks_the_closer_variant(self):
        words_form, digits_form = "закажи двенадцать стульев", "закажи 12 стульев"
        errors, _, variant = score.best_variant([words_form, digits_form], "Закажи 12 стульев.")
        self.assertEqual(errors, 0)
        self.assertEqual(variant, digits_form)

    def test_char_errors(self):
        self.assertEqual(score.char_errors(["кот"], "кит"), (1, 3))


def references():
    return {
        "ru01": {"lang": "ru", "tags": {"quiet"}, "variants": ["раз два три четыре"]},
        "ru05": {"lang": "ru", "tags": {"quiet", "numbers"}, "variants": ["закажи двенадцать стульев", "закажи 12 стульев"]},
        "en01": {"lang": "en", "tags": {"quiet"}, "variants": ["call me later"]},
        "long01": {"lang": "ru", "tags": {"quiet", "long"}, "variants": ["один два три четыре пять шесть"]},
        "sil01": {"lang": "ru", "tags": {"silence"}, "variants": [""]},
    }


def result(text, **extra):
    row = {"text": text, "audio_s": 5.0, "cpu_s": 1.0, "infer_s": 1.0, "wall_s": 1.0, "same_text_every_run": True}
    row.update(extra)
    return row


class ScoreRun(unittest.TestCase):
    def test_perfect_answers(self):
        files = {"ru01": result("Раз, два, три, четыре."), "ru05": result("Закажи 12 стульев"), "en01": result("Call me later."),
                 "long01": result("один два три четыре пять шесть"), "sil01": result("")}
        scored = score.score_run(references(), files)
        # В этом наборе нет записей с шумом и смешанных: у пустых групп нет значения (None), у остальных нули.
        self.assertEqual({name: value for name, value in scored["groups"].items() if value is not None},
                         {"рус": 0, "англ": 0, "числа": 0, "длинная": 0, "всего": 0})
        self.assertIsNone(scored["groups"]["шум"])
        self.assertEqual(scored["cer"], 0)
        self.assertEqual(scored["silence_words"], 0)

    def test_groups_are_separate_and_numbers_stay_out_of_the_main_figure(self):
        files = {"ru01": result("раз два три пять"),            # 1 ошибка из 4 слов
                 "ru05": result("закажи стульев"),               # числа: 1 пропуск из 3 (лучший вариант - слова)
                 "en01": result("call me later"),
                 "long01": result("один два три четыре пять"),   # 1 пропуск из 6
                 "sil01": result("")}
        groups = score.score_run(references(), files)["groups"]
        self.assertAlmostEqual(groups["рус"], 2 / 10)       # ru01 + long01: 2 ошибки из 4 + 6 слов
        self.assertEqual(groups["англ"], 0)
        self.assertAlmostEqual(groups["числа"], 1 / 3)
        self.assertAlmostEqual(groups["длинная"], 1 / 6)
        self.assertAlmostEqual(groups["всего"], 2 / 13)     # без записи с числами

    def test_silence_counts_invented_words(self):
        files = {"sil01": result("Спасибо за просмотр")}
        scored = score.score_run(references(), files)
        self.assertEqual((scored["silence_files"], scored["silence_words"]), (1, 3))

    def test_missing_results_are_reported_not_scored(self):
        scored = score.score_run(references(), {"ru01": result("раз два три четыре")})
        self.assertIn("en01", scored["missing"])
        self.assertIsNone(scored["groups"]["англ"])

    def test_language_detection_only_counts_when_no_hint_was_given(self):
        files = {"ru01": result("раз два три четыре", language="ru", lang_hint=None),
                 "en01": result("call me later", language="ru", lang_hint=None),
                 "long01": result("один", language="en", lang_hint="ru")}
        self.assertEqual(score.score_run(references(), files)["language"], (1, 2))


class Performance(unittest.TestCase):
    def test_cpu_seconds_per_audio_minute(self):
        files = {"a": result("x", audio_s=30.0, cpu_s=3.0, infer_s=2.0), "b": result("y", audio_s=30.0, cpu_s=3.0, infer_s=4.0)}
        perf = score.performance(files, {"peak_mb_final": 900.0, "load_s": 1.5, "cpus_visible": 2})
        self.assertAlmostEqual(perf["cpu_per_min"], 6.0)
        self.assertAlmostEqual(perf["rtf"], 0.1)
        self.assertEqual(perf["peak_mb"], 900.0)
        self.assertEqual(perf["stable"], 1.0)


    def test_a_model_loaded_on_every_call_adds_that_time_to_the_load(self):
        files = {"a": result("x", call_load_s=0.3), "b": result("y", call_load_s=0.5), "c": result("z", call_load_s=0.4)}
        perf = score.performance(files, {"load_s": 0.0, "cpus_visible": 1})
        self.assertAlmostEqual(perf["load_s"], 0.4)  # медиана

    def test_files_without_a_per_call_load_leave_the_load_alone(self):
        perf = score.performance({"a": result("x")}, {"load_s": 1.5, "cpus_visible": 1})
        self.assertAlmostEqual(perf["load_s"], 1.5)


class Scenario(unittest.TestCase):
    """references.tsv (машинный) и phrases.md (человеческий) должны говорить об одних и тех же записях."""

    def test_every_reference_is_in_the_script_and_back(self):
        refs = score.load_references(os.path.join(HERE, "references.tsv"))
        with open(os.path.join(HERE, "phrases.md"), encoding="utf-8") as f:
            script = f.read()
        in_script = set(re.findall(r"\| `([a-z]+\d+)` \|", script))
        self.assertEqual(set(refs), in_script)

    def test_ids_have_a_known_language_prefix(self):
        for id_ in score.load_references(os.path.join(HERE, "references.tsv")):
            self.assertRegex(id_, r"^(ru|en|mix|long|sil)\d+$")

    def test_reference_text_matches_the_script(self):
        refs = score.load_references(os.path.join(HERE, "references.tsv"))
        with open(os.path.join(HERE, "phrases.md"), encoding="utf-8") as f:
            script = f.read()
        for id_, ref in refs.items():
            if "silence" not in ref["tags"]:
                self.assertIn(ref["variants"][0], script, id_)


if __name__ == "__main__":
    unittest.main()
