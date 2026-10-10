"""Тесты сводки памяти на искусственных прогонах: ровная кривая, растущая, оборванная убитым контейнером."""
import json
import os
import tempfile
import unittest

import memory_report


def call(n, pass_, rss, anon=None, cg=None, file="a.wav", oom=0):
    anon = rss - 50 if anon is None else anon
    return {"event": "call", "call": n, "pass": pass_, "file": file, "rss_mb": rss, "hwm_mb": rss,
            "anon_mb": anon, "cg_mb": cg or rss + 700, "cg_peak_mb": cg or rss + 700, "oom_kill": oom}


def write_run(folder, name, per_pass_rss, files=2, limit=3072, exit_line="oom_killed=false exit_code=0", done=True,
              tail=""):
    path = os.path.join(folder, name + ".jsonl")
    n = 0
    with open(path, "w", encoding="utf-8") as f:
        f.write(json.dumps({"event": "loaded", "files": files, "passes": len(per_pass_rss), "limit_mb": limit,
                            "rss_mb": 1200, "cg_mb": 1900}) + "\n")
        for p, rss in enumerate(per_pass_rss, 1):
            for _ in range(files):
                n += 1
                f.write(json.dumps(call(n, p, rss)) + "\n")
        if done:
            f.write(json.dumps({"event": "done", "calls": n}) + "\n")
        f.write(tail)
    with open(os.path.join(folder, name + ".exit"), "w", encoding="utf-8") as f:
        f.write(exit_line + "\n")
    return path


class MemoryReportTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)

    def run_of(self, name, rss, **kw):
        return memory_report.summarize(write_run(self.dir.name, name, rss, **kw))

    def test_flat_curve_is_a_ceiling(self):
        r = self.run_of("flat", [1700, 1900, 2000, 2000, 2005, 2005, 2005, 2005])
        self.assertEqual(r["verdict"], "потолок")
        self.assertEqual(r["peak_rss"], 2005)
        self.assertEqual(r["calls"], r["expected"])
        self.assertFalse(r["oom_killed"])

    def test_growing_curve_is_reported(self):
        r = self.run_of("grow", [1700, 1800, 1900, 2000, 2100, 2200, 2300, 2400])
        self.assertEqual(r["verdict"], "растёт")
        self.assertEqual(r["growth_mb"], 400)  # от конца 4-го прохода (2000) до конца 8-го (2400)

    def test_growth_only_in_the_first_half_is_a_ceiling(self):
        r = self.run_of("early", [1500, 1700, 1900, 2000, 2000, 2000, 2000, 2000])
        self.assertEqual(r["verdict"], "потолок")

    def test_too_few_passes_are_not_judged(self):
        self.assertEqual(self.run_of("few", [1700, 1800, 1900])["verdict"], "мало данных")

    def test_headroom_is_measured_against_the_limit(self):
        r = self.run_of("room", [1700, 1800, 1900, 2000], limit=3072)
        self.assertEqual(r["headroom_anon"], 3072 - 1950)
        self.assertEqual(r["headroom_cg"], 3072 - 2700)

    def test_killed_container_with_a_cut_last_line(self):
        r = self.run_of("killed", [1700, 1900, 2100, 2300], exit_line="oom_killed=true exit_code=137", done=False,
                        tail='{"event": "call", "call": 99, "pas')
        self.assertTrue(r["oom_killed"])
        self.assertEqual(r["exit_code"], 137)
        self.assertFalse(r["finished"])
        self.assertEqual(r["calls"], 8)  # оборванная строка не считается и не ломает чтение

    def test_missing_exit_file_is_unknown(self):
        path = write_run(self.dir.name, "noexit", [1700, 1800, 1900, 2000])
        os.remove(os.path.join(self.dir.name, "noexit.exit"))
        self.assertIsNone(memory_report.summarize(path)["oom_killed"])

    def test_raises_name_the_calls_that_lifted_the_peak(self):
        loaded = {"hwm_mb": 1200}
        calls = [call(1, 1, 1210, file="a.wav"), call(2, 1, 1600, file="long.wav"), call(3, 1, 1605, file="b.wav"),
                 call(4, 2, 1990, file="long.wav")]
        self.assertEqual(memory_report.raises(loaded, calls, 20), [(2, 1, "long.wav", 390), (4, 2, "long.wav", 385)])
        self.assertEqual(memory_report.raises(loaded, calls, 5)[-1][0], 4)

    def test_last_raise_marks_where_the_plateau_starts(self):
        r = self.run_of("plateau", [1700, 1900, 2000, 2000, 2000, 2000, 2000, 2000], files=2)
        self.assertEqual(r["last_raise_call"], 5)  # первый вызов 3-го прохода: 2000 против 1900
        self.assertEqual(r["big_raises"][0][0], 1)

    def test_per_pass_keeps_the_largest_value_inside_a_pass(self):
        calls = [call(1, 1, 1500), call(2, 1, 1800), call(3, 2, 1700)]
        passes = memory_report.per_pass(calls)
        self.assertEqual(passes[1]["rss"], 1800)
        self.assertEqual(passes[2]["rss"], 1700)


if __name__ == "__main__":
    unittest.main()
