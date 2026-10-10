#!/usr/bin/env python3
"""A one-off check of the speech recognition image on the machine it will run on (the production VPS).

It repeats on that machine what bench/stt did on the owner's Mac: the real image (stt/) is run with the production
limits (memory 3 GB without swap, a CPU quota), the owner's recordings are sent to it through the real protocol, and
the speed, the memory and the answers are measured. Nothing here belongs to the service and nothing is deployed:
containers are started from the image, used and removed.

    python3 check.py                         # everything: limits, speed at 1 and 2 cores, memory soak
    python3 check.py --quick                 # limits and speed only, about five minutes
    python3 check.py --cpus 2 --passes 3

Standard library only (Python 3.8 or newer) and the `docker` command. See README.md for what it does to the machine,
how to read the result and what it does not check."""
from __future__ import annotations

import argparse
import contextlib
import dataclasses
import difflib
import glob
import io
import json
import os
import platform
import random
import re
import secrets
import signal
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import wave
from typing import Callable, Dict, Iterator, List, Optional, Tuple

MIB = 1 << 20
GIB = 1 << 30

CONTAINER = "pigeondrop-stt-check"
OOM_CONTAINER = "pigeondrop-stt-check-oom"
# The lengths (seconds) cut from the 60 second recording and added to every pass of the memory soak: the peak of the
# model's memory depends on the length of a recording, and a few long ones do not show what mixed lengths do
# (STT_COMPARISON.md, 7.4).
VARIED_SECONDS = (8, 14, 20, 26, 32, 38, 44, 50, 56)

# What the verdict compares with (README.md, "Как читать результат").
HEADROOM_OK_MIB = 512        # memory left below the limit at the container's peak
HEADROOM_WARN_MIB = 128
LATENCY_OK_S = 30.0          # a 60 second recording; the Go server waits 90 s (STT_TIMEOUT)
LATENCY_WARN_S = 60.0
HOST_START_MARGIN_MIB = 512  # the host must have this much available beyond the container's limit to start
HOST_ABORT_MIB = 300         # below this the container is killed: it must never take memory from the other apps
HOST_WARN_MIB = 1024
RAISE_MIB = 5                # a rise of the peak that counts (as in memory_report.py of the stand)
MIN_SOAK_CALLS = 30          # fewer calls cannot show that the memory has stopped growing


# Verdict statuses, as printed in the report.
OK, WARN, FAIL, SKIP = "ОК", "ВНИМАНИЕ", "СБОЙ", "не проверялось"


class CheckError(RuntimeError):
    """Something the owner must fix or know before the check can mean anything."""


def say(message: str) -> None:
    print(f"[{time.strftime('%H:%M:%S')}] {message}", flush=True)


# --- reading the machine ----------------------------------------------------------------------------------------


def parse_meminfo(text: str) -> Dict[str, int]:
    """/proc/meminfo as bytes by name."""
    values = {}
    for line in text.splitlines():
        match = re.match(r"(\w+):\s+(\d+)\s*kB", line)
        if match:
            values[match.group(1)] = int(match.group(2)) * 1024
    return values


def read_text(path: str) -> Optional[str]:
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return None


def host_available() -> Optional[int]:
    """Memory the host can give out without swapping, or None when this is not Linux (a developer's Mac)."""
    return parse_meminfo(read_text("/proc/meminfo") or "").get("MemAvailable")


def parse_cpu_times(text: str) -> Optional[Tuple[int, int]]:
    """(steal, total) jiffies from the first line of /proc/stat. Steal is the time a virtual machine wanted a core and
    the host gave it to someone else: the sign of a noisy neighbour."""
    for line in text.splitlines():
        if line.startswith("cpu "):
            fields = [int(x) for x in line.split()[1:] if x.isdigit()]
            if len(fields) >= 8:
                return fields[7], sum(fields[:8])
    return None


def steal_percent(before: Optional[Tuple[int, int]], after: Optional[Tuple[int, int]]) -> Optional[float]:
    if not before or not after or after[1] <= before[1]:
        return None
    return 100.0 * (after[0] - before[0]) / (after[1] - before[1])


INTERESTING_FLAGS = ("avx2", "fma", "avx512f", "avx512_vnni", "avx_vnni", "asimd", "neon")


def cpu_description(cpuinfo: str) -> Tuple[str, List[str]]:
    model, flags = "unknown", []
    for line in cpuinfo.splitlines():
        key, _, value = line.partition(":")
        key, value = key.strip().lower(), value.strip()
        if key in ("model name", "model") and model == "unknown" and value:
            model = value
        if key in ("flags", "features") and not flags:
            have = value.split()
            flags = [f for f in INTERESTING_FLAGS if f in have]
    return model, flags


def docker(*args: str, timeout: Optional[float] = 120, check: bool = True) -> subprocess.CompletedProcess:
    try:
        done = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout)
    except FileNotFoundError:
        raise CheckError("команда docker не найдена: Docker не установлен или его нет в PATH")
    except subprocess.TimeoutExpired:
        raise CheckError(f"`docker {' '.join(args[:2])}` не закончилась за {timeout} с")
    if check and done.returncode != 0:
        raise CheckError(f"`docker {' '.join(args[:2])}` завершилась с ошибкой: {done.stderr.strip() or done.stdout.strip()}")
    return done


def docker_info() -> dict:
    return json.loads(docker("info", "--format", "{{json .}}").stdout)


def collect_facts(info: dict) -> dict:
    meminfo = parse_meminfo(read_text("/proc/meminfo") or "")
    model, flags = cpu_description(read_text("/proc/cpuinfo") or "")
    try:
        load = [round(x, 2) for x in os.getloadavg()]
    except OSError:
        load = None
    in_mib = lambda key: meminfo[key] // MIB if key in meminfo else None  # noqa: E731
    return {
        "machine": platform.machine(), "kernel": platform.release(), "cpu_model": model, "cpu_flags": flags,
        "cores": os.cpu_count(), "load_average": load,
        "mem_total_mib": in_mib("MemTotal"), "mem_available_mib": in_mib("MemAvailable"),
        "swap_total_mib": in_mib("SwapTotal"), "swap_free_mib": in_mib("SwapFree"),
        "docker_version": info.get("ServerVersion"), "docker_cgroup": info.get("CgroupVersion"),
        "docker_cgroup_driver": info.get("CgroupDriver"), "docker_cpus": info.get("NCPU"),
        "running_containers": len([x for x in docker("ps", "-q").stdout.split() if x]),
    }


# --- recordings -------------------------------------------------------------------------------------------------


@dataclasses.dataclass(frozen=True)
class Clip:
    name: str
    data: bytes
    seconds: float


def wav_info(data: bytes) -> Tuple[int, int, int, int]:
    """(rate, channels, sample width, frames); raises CheckError for a file that is not a WAV."""
    try:
        with wave.open(io.BytesIO(data)) as w:
            return w.getframerate(), w.getnchannels(), w.getsampwidth(), w.getnframes()
    except (wave.Error, EOFError) as error:
        raise CheckError(f"это не WAV-файл: {error}")


def make_clip(name: str, data: bytes) -> Clip:
    rate, channels, width, frames = wav_info(data)
    if (rate, channels, width) != (16000, 1, 2):
        raise CheckError(f"{name}: сервис принимает WAV 16 кГц, 16 бит, моно, а здесь {rate} Гц, каналов {channels}, "
                         f"{width * 8} бит")
    return Clip(name, data, frames / rate)


def slice_wav(data: bytes, seconds: float) -> bytes:
    """The first `seconds` of a WAV, in the same format."""
    with wave.open(io.BytesIO(data)) as source:
        frames = source.readframes(int(seconds * source.getframerate()))
        out = io.BytesIO()
        with wave.open(out, "wb") as target:
            target.setnchannels(source.getnchannels())
            target.setsampwidth(source.getsampwidth())
            target.setframerate(source.getframerate())
            target.writeframes(frames)
    return out.getvalue()


def load_clips(directory: str) -> Tuple[List[Clip], Clip, List[Clip]]:
    """The recordings (data/wav), the 60 second one (data/stress/max60.wav) and the lengths cut from it."""
    paths = sorted(glob.glob(os.path.join(directory, "wav", "*.wav")))
    longest = os.path.join(directory, "stress", "max60.wav")
    if not paths:
        raise CheckError(f"нет записей в {os.path.join(directory, 'wav')}")
    if not os.path.isfile(longest):
        raise CheckError(f"нет файла {longest} (запись на 60 секунд)")
    clips = []
    for path in paths:
        with open(path, "rb") as f:
            clips.append(make_clip(os.path.splitext(os.path.basename(path))[0], f.read()))
    with open(longest, "rb") as f:
        raw = f.read()
    max60 = make_clip("max60", raw)
    varied = [make_clip(f"len{sec}", slice_wav(raw, sec)) for sec in VARIED_SECONDS if sec < max60.seconds]
    return clips, max60, varied


def load_reference(path: Optional[str]) -> Dict[str, str]:
    """The texts the stand got on the owner's Mac, by recording name (bench/stt/results/*.jsonl)."""
    texts: Dict[str, str] = {}
    if not path or not os.path.isfile(path):
        return texts
    with open(path, encoding="utf-8") as f:
        for line in f:
            row = json.loads(line)
            if "file" in row:
                texts[os.path.splitext(row["file"])[0]] = row.get("text", "").strip()
    return texts


# --- the service ------------------------------------------------------------------------------------------------


@dataclasses.dataclass
class Reply:
    status: int  # 0: no answer at all
    body: dict
    seconds: float
    retry_after: Optional[str] = None


def transcribe(base: str, token: str, data: bytes, timeout: float = 300) -> Reply:
    request = urllib.request.Request(base + "/v1/transcribe", data=data, method="POST",
                                     headers={"Content-Type": "audio/wav", "Authorization": "Bearer " + token})
    started = time.perf_counter()

    def parse(raw: bytes) -> dict:
        try:
            return json.loads(raw or b"{}")
        except ValueError:
            return {}

    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return Reply(response.status, parse(response.read()), time.perf_counter() - started,
                         response.headers.get("Retry-After"))
    except urllib.error.HTTPError as error:
        return Reply(error.code, parse(error.read()), time.perf_counter() - started, error.headers.get("Retry-After"))
    except (urllib.error.URLError, OSError) as error:  # refused, reset, timed out
        return Reply(0, {"error": str(error)}, time.perf_counter() - started)


def get_status(url: str) -> int:
    try:
        with urllib.request.urlopen(url, timeout=3) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
    except (urllib.error.URLError, OSError):
        return 0


# --- the container ----------------------------------------------------------------------------------------------

SNAPSHOT_SCRIPT = ('for f in memory.current memory.peak memory.stat memory.events cpu.stat; do '
                   'echo "## $f"; cat /sys/fs/cgroup/$f 2>/dev/null; done')


@dataclasses.dataclass(frozen=True)
class Snapshot:
    current: int         # everything charged to the container, the cache of files included
    peak: Optional[int]  # the highest `current` so far; the kernel keeps it since 5.19, otherwise None
    anon: int            # the process's own memory: what cannot be dropped when the limit is near
    file: int
    oom_kill: int
    cpu_usec: int


def parse_snapshot(text: str) -> Snapshot:
    """The output of SNAPSHOT_SCRIPT (cgroup v2 files of the container)."""
    sections: Dict[str, List[str]] = {}
    name = None
    for line in text.splitlines():
        if line.startswith("## "):
            name = line[3:].strip()
            sections[name] = []
        elif name:
            sections[name].append(line)

    def number(section: str) -> Optional[int]:
        lines = [x.strip() for x in sections.get(section, []) if x.strip()]
        return int(lines[0]) if lines and lines[0].isdigit() else None

    def keyed(section: str) -> Dict[str, int]:
        values = {}
        for line in sections.get(section, []):
            parts = line.split()
            if len(parts) == 2 and parts[1].isdigit():
                values[parts[0]] = int(parts[1])
        return values

    current = number("memory.current")
    if current is None:
        raise ValueError("the container has no memory.current: this is not cgroup v2")
    stat, events, cpu = keyed("memory.stat"), keyed("memory.events"), keyed("cpu.stat")
    return Snapshot(current=current, peak=number("memory.peak"), anon=stat.get("anon", 0), file=stat.get("file", 0),
                    oom_kill=events.get("oom_kill", 0), cpu_usec=cpu.get("usage_usec", 0))


def parse_limits(text: str, threads: Optional[int]) -> dict:
    """`cat memory.max memory.swap.max cpu.max; nproc` inside the container."""
    tokens = text.split()
    if len(tokens) < 5:
        raise ValueError(f"unexpected limits output: {text!r}")
    number = lambda token: int(token) if token.isdigit() else None  # noqa: E731 ("max" is no limit)
    return {"memory_max": number(tokens[0]), "swap_max": number(tokens[1]), "cpu_quota_us": number(tokens[2]),
            "cpu_period_us": number(tokens[3]), "visible_cpus": number(tokens[4]), "threads": threads}


class Container:
    """One container of the image, started the way Coolify will start it: a memory limit without swap, a CPU quota
    (not pinned cores), the token in the environment, the port reachable only from this machine."""

    def __init__(self, image: str, cpus: int, memory_gib: int, name: str = CONTAINER):
        self.image, self.cpus, self.memory_gib, self.name = image, cpus, memory_gib, name
        self.token = secrets.token_hex(16)
        self.base = ""
        self.ready_seconds: Optional[float] = None
        self.limits: Optional[dict] = None
        self.final: dict = {}

    def start(self) -> None:
        docker("rm", "-f", self.name, check=False)
        memory = f"{self.memory_gib}g"
        docker("run", "-d", "--name", self.name, "--memory", memory, "--memory-swap", memory, "--cpus", str(self.cpus),
               "-p", "127.0.0.1::8000", "-e", f"STT_TOKEN={self.token}", self.image)
        started = time.monotonic()
        mapping = docker("port", self.name, "8000/tcp").stdout.split()[0]
        self.base = "http://127.0.0.1:" + mapping.rsplit(":", 1)[1]
        deadline = started + 180
        while time.monotonic() < deadline:
            if get_status(self.base + "/healthz") == 200:
                self.ready_seconds = time.monotonic() - started
                return
            if "true" not in docker("inspect", "-f", "{{.State.Running}}", self.name, check=False).stdout:
                raise CheckError("контейнер остановился при запуске:\n" + self.logs())
            time.sleep(0.5)
        raise CheckError("модель не загрузилась за 180 с:\n" + self.logs())

    def logs(self) -> str:
        done = docker("logs", "--tail", "8", self.name, check=False)
        return (done.stdout + done.stderr).strip()

    def snapshot(self) -> Snapshot:
        return parse_snapshot(docker("exec", self.name, "sh", "-c", SNAPSHOT_SCRIPT, timeout=30).stdout)

    def read_limits(self) -> dict:
        text = docker("exec", self.name, "sh", "-c",
                      "cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/cpu.max; nproc").stdout
        threads = docker("exec", self.name, "python", "-c",
                         "from app.cpus import available_cpus; print(available_cpus())", check=False).stdout.strip()
        self.limits = parse_limits(text, int(threads) if threads.isdigit() else None)
        return self.limits

    def kill(self) -> None:
        docker("kill", self.name, check=False)

    def remove(self) -> None:
        state = docker("inspect", "-f", "{{.State.OOMKilled}} {{.State.ExitCode}} {{.State.Running}}", self.name,
                       check=False).stdout.split()
        if len(state) == 3:
            self.final = {"oom_killed": state[0] == "true", "exit_code": int(state[1]), "running": state[2] == "true"}
        docker("rm", "-f", self.name, check=False)


class Watchdog(threading.Thread):
    """Watches the machine while a container works: the container is killed if the host is about to run out of
    memory (the check must never cost the other apps on a shared VPS their memory). Samples the container's memory
    too when `sample_container` is set."""

    def __init__(self, container, interval: float = 1.0, abort_below: int = HOST_ABORT_MIB * MIB,
                 available: Optional[Callable[[], Optional[int]]] = None, sample_container: bool = False):
        super().__init__(daemon=True)
        self.container, self.interval, self.abort_below = container, interval, abort_below
        self.available = available if available is not None else host_available  # looked up now, not at import
        self.sample_container = sample_container
        self.stopping = threading.Event()
        self.min_available: Optional[int] = None
        self.tripped: Optional[str] = None
        self.samples: List[dict] = []

    def run(self) -> None:
        while not self.stopping.wait(self.interval):
            self.tick()

    def tick(self) -> None:
        free = self.available()
        if free is not None:
            self.min_available = free if self.min_available is None else min(self.min_available, free)
            if free < self.abort_below and not self.tripped:
                self.tripped = f"на машине осталось только {free // MIB} МиБ свободной памяти"
                self.container.kill()
                return
        if not self.sample_container:
            return
        try:
            snap = self.container.snapshot()
        except (CheckError, ValueError):
            return
        self.samples.append({"t": round(time.time(), 1), "current": snap.current, "anon": snap.anon, "peak": snap.peak,
                             "host_available": free})

    def stop(self) -> None:
        self.stopping.set()
        if self.is_alive():
            self.join(timeout=10)


@contextlib.contextmanager
def running(container: Container, sample_container: bool = False) -> Iterator[Watchdog]:
    """Starts the container, waits for the model, watches the host; always removes the container."""
    watchdog = Watchdog(container, sample_container=sample_container)
    try:
        container.start()
        container.read_limits()
        watchdog.start()
        yield watchdog
    finally:
        watchdog.stop()
        container.remove()


# --- measuring --------------------------------------------------------------------------------------------------


def measured_request(container: Container, clip: Clip, step: str, **extra) -> dict:
    """One recording through the service, with the container's CPU time and memory around it."""
    row = {"step": step, "file": clip.name, "audio_s": round(clip.seconds, 2), **extra}
    try:
        before = container.snapshot()
        reply = transcribe(container.base, container.token, clip.data)
        after = container.snapshot()
    except (CheckError, ValueError) as error:  # the container is gone
        row.update(status=0, wall_s=0.0, error=str(error))
        return row
    row.update(status=reply.status, wall_s=round(reply.seconds, 3), cpu_s=round((after.cpu_usec - before.cpu_usec) / 1e6, 3),
               mem_current=after.current, mem_anon=after.anon, mem_peak=after.peak)
    if reply.status == 200:
        row.update(stt_s=round(reply.body.get("stt_ms", 0) / 1000, 3), text=str(reply.body.get("text", "")).strip())
    else:
        row["error"] = str(reply.body)[:200]
    return row


def median_or_none(values: List[float]) -> Optional[float]:
    return statistics.median(values) if values else None


def speed_stats(rows: List[dict]) -> dict:
    ok = [r for r in rows if r["status"] == 200]
    audio = sum(r["audio_s"] for r in ok)
    wall = sum(r["wall_s"] for r in ok)
    cpu = sum(r.get("cpu_s", 0.0) for r in ok)
    short = [r["wall_s"] for r in ok if r["audio_s"] < 10]
    long50 = [r["wall_s"] for r in ok if r["file"] == "long01"]
    long60 = [r["wall_s"] for r in ok if r["file"] == "max60"]
    return {"calls": len(rows), "failed": len(rows) - len(ok), "audio_s": audio, "wall_s": wall,
            "audio_per_second": audio / wall if wall else None,
            "cpu_s_per_audio_min": cpu / audio * 60 if audio else None,
            "short_median_s": median_or_none(short), "short_max_s": max(short) if short else None,
            "long50_median_s": median_or_none(long50),
            "long60_median_s": median_or_none(long60), "long60_max_s": max(long60) if long60 else None}


def word_edits(a: str, b: str) -> int:
    """How many words differ between two texts (an insertion, a deletion or a replacement of a run counts by the
    longer side)."""
    x, y = a.split(), b.split()
    edits = 0
    for tag, i1, i2, j1, j2 in difflib.SequenceMatcher(None, x, y, autojunk=False).get_opcodes():
        if tag != "equal":
            edits += max(i2 - i1, j2 - j1)
    return edits


def compare_texts(rows: List[dict], reference: Dict[str, str]) -> dict:
    """The texts of the first answer for each recording against the stand's, and whether a recording got the same
    text in every round."""
    seen: Dict[str, List[str]] = {}
    for row in rows:
        if row["status"] == 200:
            seen.setdefault(row["file"], []).append(row["text"])
    compared, identical, diffs = 0, 0, []
    for name, texts in seen.items():
        if name not in reference:
            continue
        compared += 1
        if texts[0] == reference[name]:
            identical += 1
        else:
            diffs.append({"file": name, "stand": reference[name], "here": texts[0], "words": word_edits(reference[name], texts[0])})
    unstable = sorted(name for name, texts in seen.items() if len(set(texts)) > 1)
    return {"compared": compared, "identical": identical, "differences": diffs, "unstable": unstable}


def running_max(values: List[int]) -> List[int]:
    out, top = [], 0
    for value in values:
        top = max(top, value)
        out.append(top)
    return out


def analyze_memory(rows: List[dict], limit: int) -> dict:
    """What the memory did over the calls, in the terms of the stand's report: the container's peak, how far it is
    from the limit, which calls raised it and whether it stopped rising."""
    ok = [r for r in rows if r["status"] == 200 and "mem_current" in r]
    if not ok:
        return {"calls": 0}
    sampled = any(r.get("mem_peak") is None for r in ok)
    # The kernel's own peak when there is one; otherwise the highest value seen after a call (a lower bound).
    peaks = running_max([r["mem_peak"] if r.get("mem_peak") is not None else r["mem_current"] for r in ok])
    raises = [i + 1 for i in range(1, len(peaks)) if peaks[i] - peaks[i - 1] >= RAISE_MIB * MIB]
    # `raises` compares neighbours, so a creep of many small steps is not lost: it is measured over the second half.
    half = len(peaks) // 2
    return {"calls": len(ok), "peak": peaks[-1], "peak_sampled": sampled, "headroom": limit - peaks[-1],
            "anon_max": max(r["mem_anon"] for r in ok), "current_max": max(r["mem_current"] for r in ok),
            "raises": raises, "last_raise": raises[-1] if raises else None,
            "growth_second_half": peaks[-1] - peaks[half - 1] if half >= 1 else 0,
            "plateau": len(peaks) >= 2 and (not raises or raises[-1] <= len(peaks) // 2)}


def pass_order(number: int, items: List[Clip], seed: int) -> List[Clip]:
    """The first pass goes through the list in order, the later ones in a random (but repeatable) order."""
    if number == 1:
        return list(items)
    shuffled = list(items)
    random.Random(seed * 1000 + number).shuffle(shuffled)
    return shuffled


def check_concurrency(container: Container, clip: Clip) -> dict:
    """Two recordings at the same moment: one must be taken and the other told to wait (429), and the service must
    work afterwards. One recording at a time is the owner's decision."""
    gate = threading.Barrier(2)
    replies: List[Optional[Reply]] = [None, None]

    def send(index: int) -> None:
        gate.wait()
        replies[index] = transcribe(container.base, container.token, clip.data)

    threads = [threading.Thread(target=send, args=(i,)) for i in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    done = [r for r in replies if r is not None]
    refused = [r for r in done if r.status == 429]
    after = transcribe(container.base, container.token, clip.data)
    return {"clip": clip.name, "statuses": sorted(r.status for r in done),
            "retry_after": [r.retry_after for r in refused], "codes": [r.body.get("code") for r in refused],
            "after_status": after.status}


# --- the steps --------------------------------------------------------------------------------------------------


def run_oom_test(image: str) -> dict:
    """Memory above the limit must kill the container (exit 137) instead of sending it to swap."""
    docker("rm", "-f", OOM_CONTAINER, check=False)
    done = docker("run", "--rm", "--name", OOM_CONTAINER, "--memory", "256m", "--memory-swap", "256m", "--entrypoint",
                  "python", image, "-c", "x = b'x' * (700 << 20); print('survived')", check=False, timeout=180)
    return {"exit_code": done.returncode, "survived": "survived" in done.stdout}


def run_speed(args, clips: List[Clip], max60: Clip, reference: Dict[str, str]) -> List[dict]:
    results = []
    for cpus in args.cpus:
        say(f"скорость: новый контейнер, {args.memory} ГБ, ядер {cpus}")
        container = Container(args.image, cpus, args.memory)
        with running(container) as watchdog:
            say(f"  модель готова через {container.ready_seconds:.1f} с после запуска; лимиты: {container.limits}")
            rows = [measured_request(container, clips[0], "speed", cpus=cpus, round=0)]  # the first call of a process
            for number in range(1, args.rounds + 1):
                for clip in clips:
                    rows.append(measured_request(container, clip, "speed", cpus=cpus, round=number))
                part = speed_stats([r for r in rows if r["round"] == number])
                say(f"  круг {number}: {part['audio_s']:.0f} с звука за {part['wall_s']:.0f} с")
            for _ in range(args.long_repeats):
                rows.append(measured_request(container, max60, "speed", cpus=cpus, round=99))
            say(f"  запись на 60 с, секунды: {[r['wall_s'] for r in rows if r['file'] == 'max60']}")
        measured = [r for r in rows if r["round"] != 0]
        results.append({"cpus": cpus, "ready_s": container.ready_seconds, "limits": container.limits,
                        "first_call_s": rows[0]["wall_s"], "stats": speed_stats(measured),
                        "texts": compare_texts(measured, reference), "rows": rows,
                        "host_min_available": watchdog.min_available, "tripped": watchdog.tripped,
                        "final": container.final})
        if watchdog.tripped:
            break
    return results


def run_memory(args, clips: List[Clip], max60: Clip, varied: List[Clip], rtf: Optional[float]) -> dict:
    items = clips + varied + [max60]
    pass_audio = sum(c.seconds for c in items)
    limit = args.memory * GIB
    estimate = f"около {args.passes * pass_audio * rtf / 60:.0f} мин" if rtf else "неизвестно (шаг скорости не запускался)"
    say(f"память: ядер {args.soak_cpus}, {args.memory} ГБ, до {args.passes} проходов по {len(items)} вызовов "
        f"({pass_audio:.0f} с звука в проходе), не дольше {args.soak_minutes} мин; ожидается {estimate}")
    container = Container(args.image, args.soak_cpus, args.memory)
    rows: List[dict] = []
    with running(container, sample_container=True) as watchdog:
        say(f"  модель готова через {container.ready_seconds:.1f} с после запуска; лимиты: {container.limits}")
        concurrency = check_concurrency(container, max(clips, key=lambda c: c.seconds))
        say(f"  две записи сразу: ответы {concurrency['statuses']}, затем {concurrency['after_status']}")
        started, last_pass, passes, stopped = time.monotonic(), None, 0, None
        for number in range(1, args.passes + 1):
            elapsed = time.monotonic() - started
            if last_pass is not None and elapsed + last_pass > args.soak_minutes * 60:
                stopped = f"истёк предел времени {args.soak_minutes} мин"
                break
            began, failures = time.monotonic(), 0
            for clip in pass_order(number, items, args.seed):
                row = measured_request(container, clip, "memory", cpus=args.soak_cpus, round=number)
                rows.append(row)
                failures = failures + 1 if row["status"] != 200 else 0
                if watchdog.tripped or failures >= 3:
                    break
            last_pass = time.monotonic() - began
            passes = number
            part = analyze_memory(rows, limit)
            if part["calls"]:
                say(f"  проход {number}: вызовов {part['calls']}, пик {part['peak'] // MIB} МиБ, "
                    f"последний подъём на вызове {part['last_raise']}, {last_pass:.0f} с")
            if watchdog.tripped or failures >= 3:
                stopped = watchdog.tripped or "сервис перестал отвечать"
                break
        last = None
        try:
            last = container.snapshot()
        except (CheckError, ValueError):
            pass
    return {"cpus": args.soak_cpus, "limits": container.limits, "limit": limit, "passes": passes,
            "stopped": stopped, "concurrency": concurrency, "analysis": analyze_memory(rows, limit),
            "stats": speed_stats(rows), "rows": rows, "oom_events": last.oom_kill if last else None,
            "host_min_available": watchdog.min_available, "tripped": watchdog.tripped, "samples": watchdog.samples,
            "final": container.final}


# --- the verdict ------------------------------------------------------------------------------------------------


@dataclasses.dataclass
class Verdict:
    status: str  # OK, WARN, FAIL or SKIP
    title: str
    detail: str


def mib(value: Optional[int]) -> str:
    return "н/д" if value is None else f"{value // MIB} МиБ"


def judge_limits(results: dict, memory_gib: int) -> Verdict:
    title = "Лимиты контейнера применяются"
    containers = [(f"{r['cpus']} ядр.", r["cpus"], r["limits"]) for r in results.get("speed", [])]
    if results.get("memory"):
        containers.append((f"{results['memory']['cpus']} ядр., память", results["memory"]["cpus"], results["memory"]["limits"]))
    if not containers:
        return Verdict(SKIP, title, "контейнер не запускался")
    problems, warnings = [], []
    for label, cpus, limits in containers:
        if limits["memory_max"] != memory_gib * GIB:
            problems.append(f"{label}: memory.max {limits['memory_max']}, ждали {memory_gib * GIB}")
        if limits["swap_max"] != 0:
            problems.append(f"{label}: memory.swap.max {limits['swap_max']}, ждали 0 (контейнер сможет уйти в подкачку)")
        quota, period = limits["cpu_quota_us"], limits["cpu_period_us"]
        if not quota or not period or abs(quota / period - cpus) > 0.01:
            problems.append(f"{label}: cpu.max {quota}/{period}, ждали {cpus} ядр.")
        if limits["threads"] != cpus:
            warnings.append(f"{label}: сервис возьмёт потоков {limits['threads']}, ждали {cpus}")
    if problems:
        return Verdict(FAIL, title, "; ".join(problems))
    if warnings:
        return Verdict(WARN, title, "; ".join(warnings))
    seen = containers[0][2]
    return Verdict(OK, title, f"memory.max {seen['memory_max'] // GIB} ГиБ, подкачки нет, квота процессора как задана, "
                              f"потоков столько же, сколько ядер по квоте (контейнер видит ядер: {seen['visible_cpus']})")


def judge(results: dict, memory_gib: int) -> List[Verdict]:
    verdicts = [judge_limits(results, memory_gib)]

    title = "Сверх лимита контейнер убивается"
    oom = results.get("oom")
    if oom is None:
        verdicts.append(Verdict(SKIP, title, "шаг не запускался"))
    elif oom["exit_code"] == 137 and not oom["survived"]:
        verdicts.append(Verdict(OK, title, "700 МиБ в контейнере на 256 МиБ: код выхода 137"))
    else:
        verdicts.append(Verdict(FAIL, title, f"код выхода {oom['exit_code']}, дожил до конца: {oom['survived']} (возможно, работает подкачка)"))

    speed, memory = results.get("speed", []), results.get("memory")
    steps = speed + ([memory] if memory else [])
    failed = sum(r["stats"]["failed"] for r in steps)
    calls = sum(r["stats"]["calls"] for r in steps)
    if calls:
        verdicts.append(Verdict(OK if not failed else FAIL, "На каждый вызов пришёл ответ 200", f"{calls - failed} из {calls}"))

    title = "Тексты совпадают с Mac"
    if speed:
        texts = speed[0]["texts"]
        if not texts["compared"]:
            verdicts.append(Verdict(SKIP, title, "эталонных текстов нет"))
        elif texts["identical"] == texts["compared"] and not texts["unstable"]:
            verdicts.append(Verdict(OK, title, f"{texts['identical']} из {texts['compared']} совпали дословно"))
        else:
            words = sum(d["words"] for d in texts["differences"])
            verdicts.append(Verdict(WARN, title, f"совпали {texts['identical']} из {texts['compared']}; различаются "
                                                 f"{len(texts['differences'])} (слов: {words}); менялись между кругами: "
                                                 f"{', '.join(texts['unstable']) or 'нет'}"))

    for r in speed:
        title = f"Запись на 60 с, ядер {r['cpus']}"
        worst = r["stats"]["long60_max_s"]
        if worst is None:
            verdicts.append(Verdict(SKIP, title, "ни один вызов не удался"))
            continue
        status = OK if worst <= LATENCY_OK_S else WARN if worst <= LATENCY_WARN_S else FAIL
        verdicts.append(Verdict(status, title, f"медиана {f1(r['stats']['long60_median_s'])} с, худший {f1(worst)} с "
                                               f"(сервер ждёт 90 с; в норме до {LATENCY_OK_S:.0f} с)"))

    if memory:
        title = f"Память при лимите {memory_gib} ГБ"
        a = memory["analysis"]
        killed = memory["final"].get("oom_killed") or (memory["oom_events"] or 0) > 0
        if not a["calls"]:
            verdicts.append(Verdict(FAIL, title, f"ни один вызов не удался ({memory['stopped'] or 'причина неизвестна'})"))
        else:
            headroom = a["headroom"] // MIB
            status = FAIL if killed or headroom < HEADROOM_WARN_MIB else WARN if headroom < HEADROOM_OK_MIB else OK
            notes = [f"пик {mib(a['peak'])}{' (по замерам, не меньше)' if a['peak_sampled'] else ''} из {mib(memory['limit'])}, "
                     f"запас {headroom} МиБ, собственная память процесса (anon) до {mib(a['anon_max'])}",
                     f"вызовов {a['calls']}, последний подъём на вызове {a['last_raise']}, рост во второй половине "
                     f"{a['growth_second_half'] // MIB} МиБ"]
            if killed:
                notes.append("контейнер убит по лимиту памяти")
            if memory["stopped"]:
                notes.append(f"остановлено раньше срока: {memory['stopped']}")
            if a["calls"] < MIN_SOAK_CALLS:
                status = WARN if status == OK else status
                notes.append(f"вызовов меньше {MIN_SOAK_CALLS}: этого мало, чтобы видеть, остановился ли рост")
            elif not a["plateau"]:
                status = WARN if status == OK else status
                notes.append("пик ещё рос во второй половине серии")
            verdicts.append(Verdict(status, title, "; ".join(notes)))
        c = memory["concurrency"]
        good = c["statuses"] == [200, 429] and c["after_status"] == 200 and c["codes"] == ["busy"]
        verdicts.append(Verdict(OK if good else FAIL, "Одна запись за раз",
                                f"две записи сразу получили {c['statuses']}, отказ с кодом {c['codes']} (Retry-After {c['retry_after']}), "
                                f"следующая обычная: {c['after_status']}"))

    title = "На машине осталась свободная память"
    hosts = [r["host_min_available"] for r in steps if r["host_min_available"] is not None]
    tripped = [r["tripped"] for r in steps if r["tripped"]]
    if tripped:
        verdicts.append(Verdict(FAIL, title, f"проверка остановлена: {tripped[0]}"))
    elif hosts:
        verdicts.append(Verdict(OK if min(hosts) >= HOST_WARN_MIB * MIB else WARN, title,
                                f"в худший момент у машины было доступно {mib(min(hosts))}"))
    else:
        verdicts.append(Verdict(SKIP, title, "память машины здесь не читается (не Linux)"))
    return verdicts


# --- the report -------------------------------------------------------------------------------------------------


def f1(value: Optional[float], digits: int = 1) -> str:
    return "н/д" if value is None else f"{value:.{digits}f}".replace(".", ",")


def render_report(results: dict, verdicts: List[Verdict]) -> str:
    facts = results["facts"]
    lines = ["# Распознавание речи на этой машине", "", "## Машина", ""]
    lines += [f"- Процессор: {facts['cpu_model']} ({facts['machine']}), ядер {facts['cores']}; флаги: {', '.join(facts['cpu_flags']) or 'н/д'}",
              f"- Ядро {facts['kernel']}; Docker {facts['docker_version']}, cgroup v{facts['docker_cgroup']} ({facts['docker_cgroup_driver']})",
              f"- Память: всего {facts['mem_total_mib']} МиБ, доступно в начале {facts['mem_available_mib']} МиБ; подкачка {facts['swap_total_mib']} МиБ",
              f"- Нагрузка (load average) в начале: {facts['load_average']}; уже работает контейнеров: {facts['running_containers']}"]
    if results.get("steal_percent") is not None:
        lines.append(f"- Время процессора, которое хозяин виртуальной машины отдал другим за время проверки (steal): {f1(results['steal_percent'], 2)}%")
    lines += ["", "## Вердикт", "", "| Итог | Проверка | Подробности |", "|---|---|---|"]
    lines += [f"| {v.status} | {v.title} | {v.detail} |" for v in verdicts]

    if results.get("speed"):
        lines += ["", "## Скорость (каждая строка: новый контейнер; на Mac с M4 Pro было 20 с звука в секунду на 2 ядрах)", "",
                  "| Ядер | Готов через, с | Первый вызов, с | Звука в секунду | CPU-с на минуту звука | Записи 4–8 с: медиана / худший, с | 49,6 с, с | 60 с: медиана / худший, с |",
                  "|---|---|---|---|---|---|---|---|"]
        for r in results["speed"]:
            s = r["stats"]
            lines.append(f"| {r['cpus']} | {f1(r['ready_s'])} | {f1(r['first_call_s'], 2)} | {f1(s['audio_per_second'])} | "
                         f"{f1(s['cpu_s_per_audio_min'])} | {f1(s['short_median_s'], 2)} / {f1(s['short_max_s'], 2)} | "
                         f"{f1(s['long50_median_s'])} | {f1(s['long60_median_s'])} / {f1(s['long60_max_s'])} |")
        diffs = [d for r in results["speed"][:1] for d in r["texts"]["differences"]]
        if diffs:
            lines += ["", "## Тексты, отличающиеся от Mac", "", "| Запись | Слов | На Mac | Здесь |", "|---|---|---|---|"]
            lines += [f"| {d['file']} | {d['words']} | {d['stand']} | {d['here']} |" for d in diffs]

    memory = results.get("memory")
    if memory and memory["analysis"]["calls"]:
        a = memory["analysis"]
        lines += ["", "## Память (на Mac: пик 2232 МиБ из 3072, последний подъём на вызове 54 из 1240)", "",
                  "| Ядер | Проходов | Вызовов | Пик, МиБ | Запас, МиБ | Своя память (anon) макс., МиБ | Последний подъём (5+ МиБ) на вызове | Рост во второй половине, МиБ | Убит |",
                  "|---|---|---|---|---|---|---|---|---|",
                  f"| {memory['cpus']} | {memory['passes']} | {a['calls']} | {a['peak'] // MIB}{'*' if a['peak_sampled'] else ''} | "
                  f"{a['headroom'] // MIB} | {a['anon_max'] // MIB} | {a['last_raise']} | {a['growth_second_half'] // MIB} | "
                  f"{'да' if memory['final'].get('oom_killed') else 'нет'} |"]
        if a["peak_sampled"]:
            lines.append("\n\\* в этом ядре нет `memory.peak`: пик это наибольшее значение, замеченное после вызова, то есть оценка снизу.")
    return "\n".join(lines) + "\n"


# --- main -------------------------------------------------------------------------------------------------------


def parse_args(argv: List[str]) -> argparse.Namespace:
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description="Проверка образа распознавания речи на этой машине (README.md)")
    ap.add_argument("--image", default="pigeondrop-stt", help="образ (по умолчанию pigeondrop-stt)")
    ap.add_argument("--data", default=os.path.join(here, "data"), help="записи: wav/*.wav и stress/max60.wav")
    ap.add_argument("--reference", default=None, help="тексты стенда с Mac (по умолчанию DATA/reference.jsonl, если есть)")
    ap.add_argument("--out", default=None, help="куда писать отчёт и сырые результаты (по умолчанию out/<время>)")
    ap.add_argument("--cpus", default="1,2", help="сколько ядер давать контейнеру на шаге скорости, через запятую (1,2)")
    ap.add_argument("--soak-cpus", type=int, default=None, help="ядер на шаге памяти (по умолчанию наибольшее из --cpus)")
    ap.add_argument("--memory", type=int, default=3, help="лимит памяти контейнера, ГБ (3)")
    ap.add_argument("--steps", default="oom,speed,memory", help="oom, speed, memory (по умолчанию все три)")
    ap.add_argument("--rounds", type=int, default=2, help="скорость: кругов по всем записям (2)")
    ap.add_argument("--long-repeats", type=int, default=3, help="скорость: вызовов с записью на 60 с (3)")
    ap.add_argument("--passes", type=int, default=6, help="память: проходов по 31 записи (6)")
    ap.add_argument("--soak-minutes", type=int, default=30, help="память: остановиться через столько минут (30)")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--quick", action="store_true", help="только лимиты и скорость, один круг: первая проба минут на пять")
    ap.add_argument("--force", action="store_true", help="запускать, даже если на машине мало свободной памяти")
    args = ap.parse_args(argv)
    if args.quick:
        args.steps, args.rounds, args.long_repeats = "oom,speed", 1, 2
    try:
        args.cpus = [int(x) for x in re.split(r"[,\s]+", args.cpus.strip()) if x]
    except ValueError:
        ap.error("--cpus: целые числа через запятую")
    if not args.cpus or min(args.cpus) < 1:
        ap.error("--cpus: нужно хотя бы одно ядро")
    args.steps = [x for x in re.split(r"[,\s]+", args.steps.strip()) if x]
    unknown = [x for x in args.steps if x not in ("oom", "speed", "memory")]
    if unknown:
        ap.error(f"--steps: неизвестные шаги {unknown}")
    args.image = with_tag(args.image)
    if args.reference is None:
        candidate = os.path.join(args.data, "reference.jsonl")
        args.reference = candidate if os.path.isfile(candidate) else None
    return args


def with_tag(image: str) -> str:
    """`name` becomes `name:latest`: with the containerd image store `docker image inspect name` does not find the
    image that `docker images` lists as `name:latest`."""
    last = image.rsplit("/", 1)[-1]
    return image if ":" in last or "@" in image else image + ":latest"


def check_start(args: argparse.Namespace, info: dict) -> None:
    if str(info.get("CgroupVersion")) != "2":
        raise CheckError(f"Docker работает с cgroup v{info.get('CgroupVersion')}, а проверка читает файлы cgroup v2 и v1 "
                         "не поддерживает. Пришлите факты выше, поддержку можно добавить.")
    docker("image", "inspect", args.image)  # a missing image is a clear error from Docker's own message
    available = host_available()
    need = args.memory * GIB + HOST_START_MARGIN_MIB * MIB
    if available is not None and available < need and not args.force:
        raise CheckError(f"у машины доступно {available // MIB} МиБ, а контейнеру разрешено до {args.memory} ГиБ; чтобы "
                         f"остальные приложения сохранили свою память, нужно не меньше {need // MIB} МиБ. Остановите "
                         "что-нибудь или запустите с --force, если уверены.")
    docker_cpus = info.get("NCPU") or os.cpu_count() or 1
    too_many = [c for c in args.cpus if c > docker_cpus]
    if too_many:
        raise CheckError(f"--cpus {too_many}: Docker видит только {docker_cpus} ядр.")
    if args.soak_cpus is None:
        args.soak_cpus = max(args.cpus)
    elif args.soak_cpus > docker_cpus:
        raise CheckError(f"--soak-cpus {args.soak_cpus}: Docker видит только {docker_cpus} ядр.")


def write_outputs(out: str, results: dict, report: str) -> None:
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "report.md"), "w", encoding="utf-8") as f:
        f.write(report)
    rows = [row for r in results.get("speed", []) for row in r["rows"]] + (results["memory"]["rows"] if results.get("memory") else [])
    with open(os.path.join(out, "requests.jsonl"), "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    if results.get("memory"):
        with open(os.path.join(out, "samples.jsonl"), "w", encoding="utf-8") as f:
            for sample in results["memory"]["samples"]:
                f.write(json.dumps(sample) + "\n")
    with open(os.path.join(out, "facts.json"), "w", encoding="utf-8") as f:
        json.dump(results["facts"], f, indent=2)


def exit_code(verdicts: List[Verdict]) -> int:
    """1 when any check failed (2 is for a check that could not go on at all)."""
    return 1 if any(v.status == FAIL for v in verdicts) else 0


def run_all(args: argparse.Namespace, out: str) -> int:
    info = docker_info()
    facts = collect_facts(info)
    say(f"{facts['cpu_model']}, ядер {facts['cores']}, доступно памяти {facts['mem_available_mib']} МиБ, "
        f"Docker {facts['docker_version']}, cgroup v{facts['docker_cgroup']}")
    check_start(args, info)
    clips, max60, varied = load_clips(args.data)
    reference = load_reference(args.reference)
    say(f"записей {len(clips)}, {sum(c.seconds for c in clips):.0f} с звука; эталонных текстов {len(reference)}")
    results: dict = {"facts": facts}
    steal_before = parse_cpu_times(read_text("/proc/stat") or "")
    try:
        if "oom" in args.steps:
            say("лимиты: 700 МиБ памяти в контейнер на 256 МиБ")
            results["oom"] = run_oom_test(args.image)
            say(f"  код выхода {results['oom']['exit_code']} (137: убит по лимиту памяти)")
        if "speed" in args.steps:
            results["speed"] = run_speed(args, clips, max60, reference)
        if "memory" in args.steps and not any(r["tripped"] for r in results.get("speed", [])):
            rtf = None
            for r in results.get("speed", []):
                if r["cpus"] == args.soak_cpus and r["stats"]["audio_per_second"]:
                    rtf = 1 / r["stats"]["audio_per_second"]
            results["memory"] = run_memory(args, clips, max60, varied, rtf)
    finally:
        for name in (CONTAINER, OOM_CONTAINER):
            docker("rm", "-f", name, check=False)
    results["steal_percent"] = steal_percent(steal_before, parse_cpu_times(read_text("/proc/stat") or ""))
    verdicts = judge(results, args.memory)
    report = render_report(results, verdicts)
    write_outputs(out, results, report)
    print("\n" + report)
    say(f"отчёт и сырые результаты: {out}")
    return exit_code(verdicts)


def main(argv: List[str]) -> int:
    args = parse_args(argv)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))  # so that `finally` removes the containers
    out = args.out or os.path.join(os.path.dirname(os.path.abspath(__file__)), "out", time.strftime("%Y%m%d-%H%M%S"))
    try:
        return run_all(args, out)
    except CheckError as error:
        print(f"\nПроверку нельзя продолжать: {error}", file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        print("\nОстановлено, контейнеры убраны.", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
