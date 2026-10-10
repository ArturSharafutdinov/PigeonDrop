"""How many cores the container may really use. The model's runtime starts as many threads as it is told, and a
container limited by `--cpus` (Coolify's "CPU limit") still sees every core of the host: threads beyond the quota
only wait to be throttled and make recognition slower, not faster."""
from __future__ import annotations

import math
import os
from typing import Optional

CGROUP_V2_CPU_MAX = "/sys/fs/cgroup/cpu.max"
CGROUP_V1_QUOTA = "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"
CGROUP_V1_PERIOD = "/sys/fs/cgroup/cpu/cpu.cfs_period_us"


def quota_cpus_v2(cpu_max: Optional[str]) -> Optional[int]:
    """cgroup v2 `cpu.max` is "<quota> <period>" in microseconds, or "max <period>" for no limit."""
    if not cpu_max:
        return None
    parts = cpu_max.split()
    if len(parts) != 2 or not parts[0].isdigit() or not parts[1].isdigit() or int(parts[1]) == 0:
        return None
    return max(1, math.ceil(int(parts[0]) / int(parts[1])))


def quota_cpus_v1(quota: Optional[str], period: Optional[str]) -> Optional[int]:
    """cgroup v1: a quota of -1 means no limit."""
    try:
        q, p = int((quota or "").strip()), int((period or "").strip())
    except ValueError:
        return None
    if q <= 0 or p <= 0:
        return None
    return max(1, math.ceil(q / p))


def _read(path: str) -> Optional[str]:
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return None


def available_cpus(affinity: Optional[int] = None, v2: Optional[str] = None, v1: Optional[tuple] = None) -> int:
    """The smaller of the cores the process may run on (cpuset) and what the CPU quota allows; at least 1.

    The arguments are for tests; in the service they are read from the system."""
    if affinity is None:
        try:
            affinity = len(os.sched_getaffinity(0))
        except AttributeError:  # not Linux (a developer's Mac)
            affinity = os.cpu_count() or 1
    if v2 is None and v1 is None:
        v2 = _read(CGROUP_V2_CPU_MAX)
        v1 = (_read(CGROUP_V1_QUOTA), _read(CGROUP_V1_PERIOD))
    limits = [affinity]
    quota = quota_cpus_v2(v2) or (quota_cpus_v1(*v1) if v1 else None)
    if quota:
        limits.append(quota)
    return max(1, min(limits))
