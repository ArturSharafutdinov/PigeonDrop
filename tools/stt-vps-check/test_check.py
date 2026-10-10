"""Tests of check.py: the parts that read the machine and the container, the arithmetic, the verdict and the report.
Run: python3 -m unittest discover -s tools/stt-vps-check   (no Docker, no model; the service is a stub)."""
import copy
import io
import json
import os
import tempfile
import threading
import time
import unittest
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock

import check
from check import FAIL, GIB, MIB, OK, SKIP, WARN


def wav_bytes(seconds, rate=16000, channels=1, width=2):
    out = io.BytesIO()
    with wave.open(out, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(width)
        w.setframerate(rate)
        w.writeframes(b"\x01\x00" * int(seconds * rate) * channels if width == 2 else b"\x01" * int(seconds * rate) * channels)
    return out.getvalue()


MEMINFO = """MemTotal:        8040044 kB
MemFree:          215772 kB
MemAvailable:    5242880 kB
SwapTotal:       2097148 kB
SwapFree:        2097148 kB
HugePages_Total:       0
"""

SNAPSHOT = """## memory.current
2339700736
## memory.peak
2400000000
## memory.stat
anon 1500000000
file 800000000
slab 1000
## memory.events
low 0
high 0
max 3
oom 0
oom_kill 1
## cpu.stat
usage_usec 123456789
user_usec 100000000
"""


class MachineTest(unittest.TestCase):
    def test_meminfo_in_bytes(self):
        info = check.parse_meminfo(MEMINFO)
        self.assertEqual(info["MemAvailable"], 5242880 * 1024)
        self.assertEqual(info["SwapTotal"], 2097148 * 1024)
        self.assertNotIn("HugePages_Total", info)  # has no unit

    def test_no_meminfo_means_not_linux(self):
        with mock.patch.object(check, "read_text", return_value=None):
            self.assertIsNone(check.host_available())

    def test_steal(self):
        before = check.parse_cpu_times("cpu  100 0 100 700 50 0 10 40 0 0\ncpu0 1 2 3\n")
        after = check.parse_cpu_times("cpu  200 0 200 1400 100 0 20 90 0 0\n")
        self.assertEqual(before, (40, 1000))
        self.assertAlmostEqual(check.steal_percent(before, after), 100.0 * 50 / 1010, places=6)
        self.assertIsNone(check.steal_percent(before, before))  # no time passed
        self.assertIsNone(check.steal_percent(None, after))
        self.assertIsNone(check.parse_cpu_times("nothing here"))

    def test_cpu_description_x86_and_arm(self):
        x86 = "processor : 0\nmodel name\t: AMD EPYC 7543\nflags\t\t: fpu sse avx2 fma avx512f avx512_vnni\n"
        self.assertEqual(check.cpu_description(x86), ("AMD EPYC 7543", ["avx2", "fma", "avx512f", "avx512_vnni"]))
        arm = "processor : 0\nFeatures\t: fp asimd evtstrm\nCPU part : 0xd0c\n"
        self.assertEqual(check.cpu_description(arm), ("unknown", ["asimd"]))
        self.assertEqual(check.cpu_description(""), ("unknown", []))

    def test_the_x86_model_number_is_not_the_name(self):
        # Real /proc/cpuinfo of an x86 VPS: "model : 1" comes before "model name".
        x86 = ("processor\t: 0\nvendor_id\t: AuthenticAMD\ncpu family\t: 25\nmodel\t\t: 1\n"
               "model name\t: AMD EPYC 7B13\nflags\t\t: fpu avx2 fma\n")
        self.assertEqual(check.cpu_description(x86), ("AMD EPYC 7B13", ["avx2", "fma"]))

    def test_a_board_name_on_arm(self):
        board = "processor : 0\nFeatures : fp asimd\nHardware : BCM2711\nModel : Raspberry Pi 4 Model B\n"
        self.assertEqual(check.cpu_description(board), ("BCM2711", ["asimd"]))


class ContainerReadingTest(unittest.TestCase):
    def test_snapshot(self):
        s = check.parse_snapshot(SNAPSHOT)
        self.assertEqual((s.current, s.peak, s.anon, s.file, s.oom_kill, s.cpu_usec),
                         (2339700736, 2400000000, 1500000000, 800000000, 1, 123456789))

    def test_snapshot_without_memory_peak(self):
        s = check.parse_snapshot(SNAPSHOT.replace("## memory.peak\n2400000000\n", "## memory.peak\n"))
        self.assertIsNone(s.peak)
        self.assertEqual(s.current, 2339700736)

    def test_snapshot_of_cgroup_v1_is_refused(self):
        with self.assertRaises(ValueError):
            check.parse_snapshot("## memory.current\n## memory.stat\n")

    def test_limits(self):
        limits = check.parse_limits("3221225472\n0\n200000 100000\n12\n", 2)
        self.assertEqual(limits, {"memory_max": 3221225472, "swap_max": 0, "cpu_quota_us": 200000,
                                  "cpu_period_us": 100000, "visible_cpus": 12, "threads": 2})

    def test_limits_without_a_limit(self):
        limits = check.parse_limits("max\nmax\nmax 100000\n12\n", None)
        self.assertIsNone(limits["memory_max"])
        self.assertIsNone(limits["cpu_quota_us"])
        with self.assertRaises(ValueError):
            check.parse_limits("3221225472 0", None)


class RecordingsTest(unittest.TestCase):
    def test_clip_needs_the_services_format(self):
        self.assertAlmostEqual(check.make_clip("a", wav_bytes(2.5)).seconds, 2.5)
        for bad in (wav_bytes(1, rate=8000), wav_bytes(1, channels=2), wav_bytes(1, width=1)):
            with self.assertRaises(check.CheckError):
                check.make_clip("bad", bad)
        with self.assertRaises(check.CheckError):
            check.make_clip("junk", b"not a wav")

    def test_slice(self):
        cut = check.slice_wav(wav_bytes(10), 4)
        self.assertEqual(check.make_clip("x", cut).seconds, 4)
        self.assertEqual(check.make_clip("x", check.slice_wav(wav_bytes(3), 8)).seconds, 3)  # shorter than asked

    def test_load(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "wav"))
            os.makedirs(os.path.join(d, "stress"))
            for name, seconds in (("b", 2), ("a", 3)):
                with open(os.path.join(d, "wav", name + ".wav"), "wb") as f:
                    f.write(wav_bytes(seconds))
            with self.assertRaises(check.CheckError):  # no 60 second recording yet
                check.load_clips(d)
            with open(os.path.join(d, "stress", "max60.wav"), "wb") as f:
                f.write(wav_bytes(60))
            clips, longest, varied = check.load_clips(d)
            self.assertEqual([c.name for c in clips], ["a", "b"])
            self.assertEqual(longest.name, "max60")
            self.assertEqual([c.seconds for c in varied], [float(s) for s in check.VARIED_SECONDS])

    def test_load_without_recordings(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(check.CheckError):
                check.load_clips(d)

    def test_reference(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "r.jsonl")
            with open(path, "w", encoding="utf-8") as f:
                f.write(json.dumps({"file": "ru01.wav", "text": " Привет. "}, ensure_ascii=False) + "\n")
                f.write(json.dumps({"summary": True}) + "\n")
            self.assertEqual(check.load_reference(path), {"ru01": "Привет."})
        self.assertEqual(check.load_reference(None), {})
        self.assertEqual(check.load_reference("/no/such/file"), {})


def row(file="a", audio=5.0, wall=1.0, cpu=2.0, status=200, text="t", **extra):
    r = {"file": file, "audio_s": audio, "wall_s": wall, "cpu_s": cpu, "status": status}
    if status == 200:
        r["text"] = text
    r.update(extra)
    return r


class StatsTest(unittest.TestCase):
    def test_speed(self):
        rows = [row(audio=5, wall=1, cpu=2), row(audio=5, wall=1, cpu=2), row(audio=60, wall=12, cpu=24, file="max60"),
                row(audio=49.6, wall=10, cpu=20, file="long01"), row(status=429)]
        s = check.speed_stats(rows)
        self.assertEqual((s["calls"], s["failed"]), (5, 1))
        self.assertAlmostEqual(s["audio_per_second"], (5 + 5 + 60 + 49.6) / 24)
        self.assertAlmostEqual(s["cpu_s_per_audio_min"], 48 / 119.6 * 60)
        self.assertEqual((s["short_median_s"], s["short_max_s"]), (1, 1))
        self.assertEqual((s["long60_median_s"], s["long60_max_s"], s["long50_median_s"]), (12, 12, 10))

    def test_speed_without_a_success(self):
        s = check.speed_stats([row(status=0)])
        self.assertIsNone(s["audio_per_second"])
        self.assertIsNone(s["long60_max_s"])

    def test_word_edits(self):
        self.assertEqual(check.word_edits("a b c", "a b c"), 0)
        self.assertEqual(check.word_edits("a b c", "a x c"), 1)
        self.assertEqual(check.word_edits("a b c d", "a d"), 2)   # two words lost
        self.assertEqual(check.word_edits("a", "a b c"), 2)       # two words added
        self.assertEqual(check.word_edits("a b c", "x y z"), 3)

    def test_texts(self):
        rows = [row("a", text="one"), row("b", text="two x"), row("c", text="three"), row("a", text="one"),
                row("b", text="two x"), row("c", text="drei"), row("d", text="nobody asked")]
        t = check.compare_texts(rows, {"a": "one", "b": "two", "c": "three", "e": "never sent"})
        self.assertEqual((t["compared"], t["identical"]), (3, 2))
        self.assertEqual([(d["file"], d["words"]) for d in t["differences"]], [("b", 1)])
        self.assertEqual(t["unstable"], ["c"])  # c answered differently in the second round

    def test_texts_ignore_failed_calls(self):
        t = check.compare_texts([row("a", status=500)], {"a": "one"})
        self.assertEqual(t["compared"], 0)


def mem_row(peak, current=None, anon=None, status=200, file=None):
    row = {"status": status, "mem_peak": peak, "mem_current": current if current is not None else (peak or 0),
           "mem_anon": anon if anon is not None else (peak or 0) // 2}
    if file is not None:
        row["mem_file"] = file
    return row


class MemoryAnalysisTest(unittest.TestCase):
    LIMIT = 3 * GIB

    def test_plateau(self):
        peaks = [1000, 1500, 2000] + [2000] * 17
        a = check.analyze_memory([mem_row(p * MIB) for p in peaks], self.LIMIT)
        self.assertEqual(a["calls"], 20)
        self.assertEqual(a["peak"], 2000 * MIB)
        self.assertEqual(a["headroom"], 3 * GIB - 2000 * MIB)
        self.assertEqual(a["raises"], [2, 3])
        self.assertEqual(a["last_raise"], 3)
        self.assertEqual(a["growth_second_half"], 0)
        self.assertTrue(a["plateau"])
        self.assertFalse(a["peak_sampled"])

    def test_still_rising(self):
        peaks = [1000 + 10 * i for i in range(20)]
        a = check.analyze_memory([mem_row(p * MIB) for p in peaks], self.LIMIT)
        self.assertEqual(a["last_raise"], 20)
        self.assertFalse(a["plateau"])
        self.assertEqual(a["growth_second_half"], 100 * MIB)

    def test_small_steps_are_not_raises_but_show_in_the_growth(self):
        peaks = [2000 + i for i in range(20)]  # 1 MiB a call: never 5 MiB at once
        a = check.analyze_memory([mem_row(p * MIB) for p in peaks], self.LIMIT)
        self.assertEqual(a["raises"], [])
        self.assertEqual(a["growth_second_half"], 10 * MIB)

    def test_a_rise_exactly_at_the_middle_is_still_a_plateau(self):
        peaks = [2000] * 9 + [2100] + [2100] * 10  # 20 calls, the last rise at call 10: the first half
        a = check.analyze_memory([mem_row(p * MIB) for p in peaks], self.LIMIT)
        self.assertEqual(a["last_raise"], 10)
        self.assertTrue(a["plateau"])

    def test_the_kernels_peak_wins_over_what_was_seen_after_calls(self):
        # The memory went up during a call and fell back before it ended: only memory.peak remembers.
        rows = [mem_row(2500 * MIB, current=1000 * MIB), mem_row(2500 * MIB, current=1200 * MIB)]
        a = check.analyze_memory(rows, self.LIMIT)
        self.assertEqual((a["peak"], a["current_max"], a["peak_sampled"]), (2500 * MIB, 1200 * MIB, False))

    def test_a_rise_in_the_second_half_is_not_a_plateau(self):
        peaks = [2000] * 10 + [2100] + [2100] * 9
        a = check.analyze_memory([mem_row(p * MIB) for p in peaks], self.LIMIT)
        self.assertEqual(a["last_raise"], 11)
        self.assertFalse(a["plateau"])

    def test_own_memory_leaves_out_the_file_cache(self):
        # 2800 charged, 700 of it the cache of the model's files: 2100 cannot be dropped.
        rows = [mem_row(2800 * MIB, current=2800 * MIB, anon=2000 * MIB, file=700 * MIB),
                mem_row(2800 * MIB, current=2700 * MIB, anon=2100 * MIB, file=700 * MIB)]
        a = check.analyze_memory(rows, self.LIMIT)
        self.assertEqual((a["own_max"], a["own_headroom"]), (2100 * MIB, 3 * GIB - 2100 * MIB))
        self.assertEqual((a["peak"], a["headroom"]), (2800 * MIB, 3 * GIB - 2800 * MIB))  # with the cache, for the report

    def test_own_memory_without_the_file_counter_is_the_processs(self):
        a = check.analyze_memory([mem_row(2800 * MIB, current=2800 * MIB, anon=2000 * MIB)], self.LIMIT)
        self.assertEqual(a["own_max"], 2000 * MIB)

    def test_without_the_kernels_peak_the_highest_current_is_used(self):
        a = check.analyze_memory([mem_row(None, current=c * MIB) for c in (1000, 2000, 1500)], self.LIMIT)
        self.assertEqual(a["peak"], 2000 * MIB)
        self.assertTrue(a["peak_sampled"])

    def test_failed_calls_are_not_measured(self):
        a = check.analyze_memory([mem_row(1000 * MIB), {"status": 0}, mem_row(1100 * MIB, status=500)], self.LIMIT)
        self.assertEqual(a["calls"], 1)
        self.assertEqual(check.analyze_memory([{"status": 0}], self.LIMIT), {"calls": 0})

    def test_order_of_passes(self):
        items = [check.Clip(str(i), b"", float(i)) for i in range(10)]
        self.assertEqual(check.pass_order(1, items, 1), items)
        second = check.pass_order(2, items, 1)
        self.assertNotEqual(second, items)
        self.assertEqual(sorted(c.name for c in second), sorted(c.name for c in items))
        self.assertEqual(check.pass_order(2, items, 1), second)          # repeatable
        self.assertNotEqual(check.pass_order(3, items, 1), second)       # but not the same every pass


class FakeContainer:
    def __init__(self, base="", token="t", snapshots=None):
        self.base, self.token, self.killed = base, token, 0
        self._snapshots = list(snapshots or [])

    def kill(self):
        self.killed += 1

    def snapshot(self):
        if not self._snapshots:
            raise check.CheckError("gone")
        return self._snapshots.pop(0)


def snap(cpu_usec=0, current=1000, peak=1000, anon=500):
    return check.Snapshot(current=current, peak=peak, anon=anon, file=0, oom_kill=0, cpu_usec=cpu_usec)


class WatchdogTest(unittest.TestCase):
    def test_kills_when_the_host_runs_low(self):
        container = FakeContainer()
        free = iter([2000 * MIB, 900 * MIB, 200 * MIB, 5000 * MIB])
        dog = check.Watchdog(container, abort_below=300 * MIB, available=lambda: next(free))
        for _ in range(2):
            dog.tick()
        self.assertEqual((container.killed, dog.tripped), (0, None))
        self.assertEqual(dog.min_available, 900 * MIB)
        dog.tick()
        self.assertEqual(container.killed, 1)
        self.assertIn("200", dog.tripped)
        dog.tick()  # recovered: it does not kill again, the first reason stays
        self.assertEqual(container.killed, 1)
        self.assertEqual(dog.min_available, 200 * MIB)

    def test_kills_once_even_if_the_host_stays_low(self):
        container = FakeContainer()
        dog = check.Watchdog(container, abort_below=300 * MIB, available=lambda: 100 * MIB)
        for _ in range(3):
            dog.tick()
        self.assertEqual(container.killed, 1)

    def test_not_linux_means_no_watching(self):
        container = FakeContainer()
        dog = check.Watchdog(container, available=lambda: None)
        dog.tick()
        self.assertEqual((container.killed, dog.tripped, dog.min_available), (0, None, None))

    def test_samples_the_container_only_when_asked(self):
        container = FakeContainer(snapshots=[snap(current=5), snap(current=6)])
        quiet = check.Watchdog(container, available=lambda: 4000 * MIB)
        quiet.tick()
        self.assertEqual(quiet.samples, [])
        loud = check.Watchdog(container, available=lambda: 4000 * MIB, sample_container=True)
        loud.tick()
        self.assertEqual([s["current"] for s in loud.samples], [5])
        loud.tick()
        loud.tick()  # the container is gone: no sample, no crash
        self.assertEqual([s["current"] for s in loud.samples], [5, 6])

    def test_thread_stops(self):
        dog = check.Watchdog(FakeContainer(), interval=0.01, available=lambda: 4000 * MIB)
        dog.start()
        time.sleep(0.05)
        dog.stop()
        self.assertFalse(dog.is_alive())


class StubService:
    """A tiny stand-in for the recognition service: one request at a time, the rest get 429."""

    def __init__(self, delay=0.3, text="привет"):
        self.busy = threading.Lock()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                if self.headers.get("Authorization") != "Bearer t":
                    return self.answer(401, {"code": "unauthorized"})
                if not outer.busy.acquire(blocking=False):
                    return self.answer(429, {"code": "busy"}, {"Retry-After": "5"})
                try:
                    time.sleep(delay)
                    self.answer(200, {"text": text, "audio_ms": 5000, "stt_ms": 250})
                finally:
                    outer.busy.release()

            def answer(self, status, payload, headers=None):
                body = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                self.end_headers()
                self.wfile.write(body)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base = "http://127.0.0.1:%d" % self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class ServiceCallsTest(unittest.TestCase):
    def setUp(self):
        self.service = StubService()
        self.addCleanup(self.service.close)
        self.clip = check.Clip("c", wav_bytes(1), 1.0)

    def test_answer(self):
        r = check.transcribe(self.service.base, "t", self.clip.data)
        self.assertEqual((r.status, r.body["text"]), (200, "привет"))
        self.assertGreater(r.seconds, 0.2)

    def test_refusals_are_answers_not_errors(self):
        self.assertEqual(check.transcribe(self.service.base, "wrong", self.clip.data).status, 401)

    def test_no_service_is_status_zero(self):
        self.service.close()
        r = check.transcribe(self.service.base, "t", self.clip.data, timeout=2)
        self.assertEqual(r.status, 0)
        self.assertIn("error", r.body)

    def test_two_at_once(self):
        c = check.check_concurrency(FakeContainer(self.service.base), self.clip)
        self.assertEqual(c["statuses"], [200, 429])
        self.assertEqual((c["codes"], c["retry_after"], c["after_status"]), (["busy"], ["5"], 200))

    def test_a_measured_call_has_cpu_and_memory(self):
        container = FakeContainer(self.service.base, snapshots=[snap(cpu_usec=1_000_000), snap(cpu_usec=3_500_000, current=2000, peak=2500, anon=900)])
        r = check.measured_request(container, self.clip, "speed", round=1)
        self.assertEqual((r["status"], r["text"], r["stt_s"], r["cpu_s"]), (200, "привет", 0.25, 2.5))
        self.assertEqual((r["mem_current"], r["mem_peak"], r["mem_anon"], r["round"]), (2000, 2500, 900, 1))

    def test_a_measured_call_of_a_dead_container(self):
        r = check.measured_request(FakeContainer(self.service.base), self.clip, "speed")
        self.assertEqual(r["status"], 0)
        self.assertIn("gone", r["error"])

    def test_a_refused_call_is_recorded_as_failed(self):
        container = FakeContainer(self.service.base, token="wrong", snapshots=[snap(), snap()])
        r = check.measured_request(container, self.clip, "speed")
        self.assertEqual(r["status"], 401)
        self.assertIn("unauthorized", r["error"])


def limits(cpus=2, memory=3 * GIB, swap=0, threads=None):
    return {"memory_max": memory, "swap_max": swap, "cpu_quota_us": cpus * 100000, "cpu_period_us": 100000,
            "visible_cpus": 12, "threads": cpus if threads is None else threads}


def speed_result(cpus=2, long60=(10.0, 12.0), failed=0, texts=None, host=4000 * MIB, tripped=None, lim=None):
    stats = {"calls": 50, "failed": failed, "long60_max_s": max(long60), "long60_median_s": sorted(long60)[len(long60) // 2],
             "audio_per_second": 10.0, "cpu_s_per_audio_min": 6.0, "short_median_s": 0.5, "short_max_s": 0.9,
             "long50_median_s": 9.0}
    return {"cpus": cpus, "ready_s": 5.0, "limits": lim or limits(cpus), "first_call_s": 1.2, "stats": stats, "rows": [],
            "texts": texts or {"compared": 21, "identical": 21, "differences": [], "unstable": []},
            "host_min_available": host, "tripped": tripped, "final": {}}


def memory_result(peak=2200 * MIB, calls=186, last_raise=40, plateau=True, killed=False, stopped=None, statuses=None,
                  oom_events=0, host=4000 * MIB, tripped=None, lim=None, sampled=False, own=1500 * MIB):
    analysis = {"calls": calls, "peak": peak, "peak_sampled": sampled, "headroom": 3 * GIB - peak, "anon_max": 1500 * MIB,
                "own_max": own, "own_headroom": 3 * GIB - own,
                "current_max": peak, "raises": [last_raise] if last_raise else [], "last_raise": last_raise,
                "growth_second_half": 0, "plateau": plateau}
    return {"cpus": 2, "limits": lim or limits(2), "limit": 3 * GIB, "passes": 6, "stopped": stopped,
            "concurrency": {"clip": "long01", "statuses": statuses or [200, 429], "retry_after": ["5"], "codes": ["busy"],
                            "after_status": 200},
            "analysis": analysis, "stats": {"calls": calls, "failed": 0}, "rows": [], "oom_events": oom_events,
            "host_min_available": host, "tripped": tripped, "samples": [], "final": {"oom_killed": killed}}


def facts():
    return {"machine": "x86_64", "kernel": "6.1.0", "cpu_model": "AMD EPYC", "cpu_flags": ["avx2"], "cores": 4,
            "load_average": [0.1, 0.2, 0.3], "mem_total_mib": 8000, "mem_available_mib": 5000, "swap_total_mib": 0,
            "swap_free_mib": 0, "docker_version": "27.0", "docker_cgroup": "2", "docker_cgroup_driver": "systemd",
            "docker_cpus": 4, "running_containers": 12}


def good_results():
    return {"facts": facts(), "oom": {"exit_code": 137, "survived": False},
            "speed": [speed_result(1, (20.0, 22.0)), speed_result(2, (10.0, 12.0))], "memory": memory_result(),
            "steal_percent": 0.4}


def by_title(verdicts):
    return {v.title: v for v in verdicts}


class VerdictTest(unittest.TestCase):
    def judge(self, results):
        return by_title(check.judge(results, 3))

    def test_a_good_machine_has_no_complaints(self):
        verdicts = check.judge(good_results(), 3)
        self.assertEqual([v.title for v in verdicts if v.status != OK], [])
        self.assertGreaterEqual(len(verdicts), 9)

    def test_limits(self):
        for name, lim, word in (("memory", limits(memory=4 * GIB), "memory.max"), ("swap", limits(swap=GIB), "swap"),
                                ("quota", {**limits(cpus=2, threads=2), "cpu_quota_us": 100000}, "cpu.max")):
            with self.subTest(name):
                r = good_results()
                r["speed"][1] = speed_result(2, lim=lim)
                v = self.judge(r)["Лимиты контейнера применяются"]
                self.assertEqual(v.status, FAIL)
                self.assertIn(word, v.detail)

    def test_threads_that_do_not_follow_the_quota_are_a_warning(self):
        r = good_results()
        r["speed"][1] = speed_result(2, lim=limits(2, threads=12))
        self.assertEqual(self.judge(r)["Лимиты контейнера применяются"].status, WARN)

    def test_memory_limit_of_the_soak_container_counts_too(self):
        r = good_results()
        r["memory"] = memory_result(lim=limits(2, memory=2 * GIB))
        self.assertEqual(self.judge(r)["Лимиты контейнера применяются"].status, FAIL)

    def test_the_oom_test(self):
        title = "Сверх лимита контейнер убивается"
        r = good_results()
        r["oom"] = {"exit_code": 0, "survived": True}
        self.assertEqual(self.judge(r)[title].status, FAIL)
        r["oom"] = {"exit_code": 137, "survived": True}  # killed code but the output says it lived: not believable
        self.assertEqual(self.judge(r)[title].status, FAIL)
        r["oom"] = None
        self.assertEqual(self.judge(r)[title].status, SKIP)

    def test_a_failed_call(self):
        r = good_results()
        r["speed"][0] = speed_result(1, failed=2)
        v = self.judge(r)["На каждый вызов пришёл ответ 200"]
        self.assertEqual(v.status, FAIL)
        self.assertIn("из", v.detail)

    def test_texts(self):
        title = "Тексты совпадают с Mac"
        r = good_results()
        r["speed"][0] = speed_result(1, texts={"compared": 21, "identical": 20, "unstable": [],
                                               "differences": [{"file": "ru01", "stand": "a", "here": "b", "words": 1}]})
        self.assertEqual(self.judge(r)[title].status, WARN)
        r["speed"][0] = speed_result(1, texts={"compared": 21, "identical": 21, "differences": [], "unstable": ["ru02"]})
        self.assertEqual(self.judge(r)[title].status, WARN)
        r["speed"][0] = speed_result(1, texts={"compared": 0, "identical": 0, "differences": [], "unstable": []})
        self.assertEqual(self.judge(r)[title].status, SKIP)

    def test_latency_thresholds(self):
        for worst, want in ((30.0, OK), (30.1, WARN), (60.0, WARN), (60.1, FAIL)):
            with self.subTest(worst=worst):
                r = good_results()
                r["speed"][0] = speed_result(1, long60=(worst - 1, worst))
                self.assertEqual(self.judge(r)["Запись на 60 с, ядер 1"].status, want)

    def test_no_successful_long_call(self):
        r = good_results()
        r["speed"][0] = speed_result(1)
        r["speed"][0]["stats"]["long60_max_s"] = None
        self.assertEqual(self.judge(r)["Запись на 60 с, ядер 1"].status, SKIP)

    def test_memory_headroom_thresholds(self):
        title = "Память при лимите 3 ГБ"
        for headroom, want in ((600, OK), (512, OK), (511, WARN), (128, WARN), (127, FAIL)):
            with self.subTest(headroom=headroom):
                r = good_results()
                r["memory"] = memory_result(own=3 * GIB - headroom * MIB)
                self.assertEqual(self.judge(r)[title].status, want)

    def test_a_peak_full_of_file_cache_is_not_a_danger(self):
        # What the first real VPS showed: 2787 MiB charged of 3072, 2069 of it the process's own, the rest the cache.
        title = "Память при лимите 3 ГБ"
        r = good_results()
        r["memory"] = memory_result(peak=2787 * MIB, own=2069 * MIB)
        v = self.judge(r)[title]
        self.assertEqual(v.status, OK)
        self.assertIn("2787", v.detail)
        self.assertIn("2069", v.detail)
        # ... but the same peak made of memory that cannot be dropped is
        r["memory"] = memory_result(peak=2787 * MIB, own=2787 * MIB)
        self.assertEqual(self.judge(r)[title].status, WARN)

    def test_memory_killed_or_stopped(self):
        title = "Память при лимите 3 ГБ"
        r = good_results()
        r["memory"] = memory_result(killed=True)
        v = self.judge(r)[title]
        self.assertEqual(v.status, FAIL)
        self.assertIn("убит", v.detail)
        r["memory"] = memory_result(oom_events=1)  # the cgroup counted a kill even if Docker did not flag it
        self.assertEqual(self.judge(r)[title].status, FAIL)
        r["memory"] = memory_result(calls=0)
        r["memory"]["analysis"] = {"calls": 0}
        self.assertEqual(self.judge(r)[title].status, FAIL)

    def test_memory_that_has_not_settled_is_a_warning(self):
        title = "Память при лимите 3 ГБ"
        r = good_results()
        r["memory"] = memory_result(plateau=False, last_raise=180)
        v = self.judge(r)[title]
        self.assertEqual(v.status, WARN)
        self.assertIn("ещё рос", v.detail)
        r["memory"] = memory_result(calls=29)
        v = self.judge(r)[title]
        self.assertEqual(v.status, WARN)
        self.assertIn("мало", v.detail)

    def test_concurrency(self):
        title = "Одна запись за раз"
        r = good_results()
        r["memory"] = memory_result(statuses=[200, 200])
        self.assertEqual(self.judge(r)[title].status, FAIL)
        r["memory"] = memory_result()
        r["memory"]["concurrency"]["after_status"] = 500
        self.assertEqual(self.judge(r)[title].status, FAIL)
        r["memory"] = memory_result()
        r["memory"]["concurrency"]["codes"] = ["something"]
        self.assertEqual(self.judge(r)[title].status, FAIL)

    def test_host_memory(self):
        title = "На машине осталась свободная память"
        r = good_results()
        r["speed"][0] = speed_result(1, host=600 * MIB)
        self.assertEqual(self.judge(r)[title].status, WARN)
        r["speed"][0] = speed_result(1, host=250 * MIB, tripped="на машине осталось только 250 МиБ")
        v = self.judge(r)[title]
        self.assertEqual(v.status, FAIL)
        self.assertIn("остановлена", v.detail)
        r = good_results()
        for step in r["speed"] + [r["memory"]]:
            step["host_min_available"] = None
        self.assertEqual(self.judge(r)[title].status, SKIP)

    def test_only_some_steps_were_run(self):
        titles = by_title(check.judge({"facts": facts(), "oom": {"exit_code": 137, "survived": False}}, 3))
        self.assertEqual(titles["Лимиты контейнера применяются"].status, SKIP)
        self.assertEqual(titles["Сверх лимита контейнер убивается"].status, OK)
        self.assertNotIn("Одна запись за раз", titles)


class ExitCodeTest(unittest.TestCase):
    def test_only_a_failure_fails_the_run(self):
        self.assertEqual(check.exit_code([check.Verdict(OK, "a", ""), check.Verdict(WARN, "b", ""), check.Verdict(SKIP, "c", "")]), 0)
        self.assertEqual(check.exit_code([check.Verdict(OK, "a", ""), check.Verdict(FAIL, "b", "")]), 1)
        self.assertEqual(check.exit_code([]), 0)


class ReportTest(unittest.TestCase):
    def test_report(self):
        results = good_results()
        results["speed"][0] = speed_result(1, texts={"compared": 21, "identical": 20, "unstable": [],
                                                    "differences": [{"file": "ru01", "stand": "на Mac", "here": "здесь", "words": 1}]})
        report = check.render_report(results, check.judge(results, 3))
        for expected in ("## Вердикт", "## Скорость", "## Память", "AMD EPYC", "ru01", "на Mac", "здесь", "0,40%", "| ВНИМАНИЕ |"):
            self.assertIn(expected, report)
        self.assertNotIn("None", report)

    def test_report_of_a_run_without_steps(self):
        report = check.render_report({"facts": facts()}, check.judge({"facts": facts()}, 3))
        self.assertIn("## Вердикт", report)
        self.assertNotIn("## Скорость", report)

    def test_the_number_style(self):
        self.assertEqual(check.f1(1.25), "1,2")
        self.assertEqual(check.f1(None), "н/д")
        self.assertEqual(check.f1(0.456, 2), "0,46")

    def test_outputs(self):
        results = good_results()
        results["speed"][0]["rows"] = [row()]
        results["memory"]["rows"] = [row(text="привет")]
        results["memory"]["samples"] = [{"t": 1.0, "current": 5}]
        with tempfile.TemporaryDirectory() as d:
            check.write_outputs(os.path.join(d, "out"), results, "REPORT")
            def read(name):
                with open(os.path.join(d, "out", name), encoding="utf-8") as f:
                    return f.read()

            self.assertEqual(read("report.md"), "REPORT")
            lines = read("requests.jsonl").splitlines()
            self.assertEqual(len(lines), 2)
            self.assertIn("привет", lines[1])  # not escaped: readable
            self.assertEqual(json.loads(read("facts.json"))["machine"], "x86_64")
            self.assertTrue(os.path.exists(os.path.join(d, "out", "samples.jsonl")))


class ContainerStartTest(unittest.TestCase):
    def start(self, cpus, memory):
        calls = []

        def fake_docker(*args, **kwargs):
            calls.append(args)
            return mock.Mock(stdout="127.0.0.1:49153\n", returncode=0)

        container = check.Container("img:latest", cpus, memory)
        with mock.patch.object(check, "docker", side_effect=fake_docker), mock.patch.object(check, "get_status", return_value=200):
            container.start()
        return container, [c for c in calls if c[0] == "run"][0]

    def test_it_is_started_the_way_coolify_will_start_it(self):
        container, run = self.start(2, 3)
        self.assertEqual(run[run.index("--memory") + 1], "3g")
        self.assertEqual(run[run.index("--memory-swap") + 1], "3g")  # equal: no swap
        self.assertEqual(run[run.index("--cpus") + 1], "2")          # a quota, not pinned cores
        self.assertNotIn("--cpuset-cpus", run)
        self.assertEqual(run[run.index("-p") + 1], "127.0.0.1::8000")  # reachable from this machine only
        self.assertTrue(any(a.startswith("STT_TOKEN=") and len(a) > 20 for a in run))
        self.assertEqual(run[-1], "img:latest")
        self.assertEqual(container.base, "http://127.0.0.1:49153")

    def test_a_fractional_limit_reaches_docker_as_it_is(self):
        _, run = self.start(1, 2.5)
        self.assertEqual((run[run.index("--memory") + 1], run[run.index("--memory-swap") + 1]), ("2.5g", "2.5g"))


class StartTest(unittest.TestCase):
    INFO = {"CgroupVersion": "2", "NCPU": 4}

    def args(self, *extra):
        return check.parse_args(["--image", "img", *extra])

    def start(self, args, info=None, available=8 * GIB):
        with mock.patch.object(check, "docker", return_value=mock.Mock(stdout="")), \
                mock.patch.object(check, "host_available", return_value=available):
            check.check_start(args, info or self.INFO)

    def test_a_fractional_memory_limit(self):
        self.assertEqual(self.args("--memory", "2.5").memory, 2.5)
        self.assertEqual(self.args().memory, 3)
        self.start(self.args("--memory", "2.5"), available=3 * GIB)  # 2.5 GiB + 512 MiB fit exactly
        with self.assertRaises(check.CheckError):
            self.start(self.args("--memory", "2.5"), available=3 * GIB - MIB)
        results = good_results()
        results["speed"][0] = speed_result(1, lim=limits(1, memory=int(2.5 * GIB)))
        results["speed"][1] = speed_result(2, lim=limits(2, memory=int(2.5 * GIB)))
        results["memory"] = memory_result(lim=limits(2, memory=int(2.5 * GIB)))
        titles = by_title(check.judge(results, 2.5))
        self.assertEqual(titles["Лимиты контейнера применяются"].status, OK)
        self.assertIn("Память при лимите 2.5 ГБ", titles)

    def test_arguments(self):
        a = self.args()
        self.assertEqual((a.cpus, a.steps, a.memory, a.rounds), ([1, 2], ["oom", "speed", "memory"], 3, 2))
        q = self.args("--quick")
        self.assertEqual((q.steps, q.rounds, q.long_repeats), (["oom", "speed"], 1, 2))
        self.assertEqual(self.args("--cpus", "1, 4").cpus, [1, 4])
        for bad in (["--cpus", "x"], ["--cpus", "0"], ["--steps", "speed,fly"]):
            with self.subTest(bad), mock.patch("sys.stderr", io.StringIO()), self.assertRaises(SystemExit):
                self.args(*bad)

    def test_image_names(self):
        for given, want in (("pigeondrop-stt", "pigeondrop-stt:latest"), ("pigeondrop-stt:check", "pigeondrop-stt:check"),
                            ("ghcr.io/x/stt", "ghcr.io/x/stt:latest"), ("localhost:5000/stt", "localhost:5000/stt:latest"),
                            ("localhost:5000/stt:1", "localhost:5000/stt:1"), ("stt@sha256:abc", "stt@sha256:abc")):
            with self.subTest(given):
                self.assertEqual(check.with_tag(given), want)
        self.assertEqual(self.args().image, "img:latest")

    def test_ok_and_the_soak_uses_the_most_cores(self):
        a = self.args("--cpus", "1,2")
        self.start(a)
        self.assertEqual(a.soak_cpus, 2)

    def test_not_enough_memory_on_the_host(self):
        a = self.args()
        with self.assertRaises(check.CheckError) as caught:
            self.start(a, available=3 * GIB)
        self.assertIn("--force", str(caught.exception))
        self.start(self.args("--force"), available=3 * GIB)  # the owner knows better
        self.start(self.args(), available=3 * GIB + 512 * MIB)  # exactly enough
        self.start(self.args(), available=None)  # not Linux: nothing to compare

    def test_cgroup_v1_is_refused(self):
        with self.assertRaises(check.CheckError) as caught:
            self.start(self.args(), info={"CgroupVersion": "1", "NCPU": 4})
        self.assertIn("v1", str(caught.exception))

    def test_more_cores_than_docker_has(self):
        with self.assertRaises(check.CheckError):
            self.start(self.args("--cpus", "1,8"))
        with self.assertRaises(check.CheckError):
            self.start(self.args("--soak-cpus", "8"))

    def test_docker_errors_are_check_errors(self):
        with mock.patch("subprocess.run", side_effect=FileNotFoundError):
            with self.assertRaises(check.CheckError):
                check.docker("ps")
        failed = mock.Mock(returncode=1, stdout="", stderr="no such image")
        with mock.patch("subprocess.run", return_value=failed):
            with self.assertRaises(check.CheckError) as caught:
                check.docker("image", "inspect", "x")
            self.assertIn("no such image", str(caught.exception))
            self.assertEqual(check.docker("image", "inspect", "x", check=False).returncode, 1)


if __name__ == "__main__":
    unittest.main()
