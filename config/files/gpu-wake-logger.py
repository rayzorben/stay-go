#!/usr/bin/env python3
"""gpu-wake-logger — record what wakes the runtime-suspended NVIDIA dGPU.

The RTX 5060 sleeps in runtime D3 when idle, but any process that enumerates
GPUs (Vulkan loader, Chromium/Electron GPU process, EGL) wakes it for ~2-3s.
This polls the PCI device's runtime_status twice a second (a sysfs read never
wakes the device) and, on every wake, logs:

  - processes started in the WINDOW seconds before the wake (the likely cause)
  - processes that hold /dev/nvidia0 now but did not at the last wake

and, on every suspend, how long the dGPU stayed awake. Output is appended to
~/.local/state/gpu-wakes.log; `gpu-wake-logger --summary` totals it by
process name.
"""

import collections
import os
import re
import sys
import time

DEVICE = "/sys/bus/pci/devices/0000:02:00.0"
STATUS = f"{DEVICE}/power/runtime_status"
LOG = os.path.expanduser("~/.local/state/gpu-wakes.log")
POLL = 0.5
WINDOW = 5.0  # seconds before a wake whose new processes are reported
HZ = os.sysconf("SC_CLK_TCK")


def read(path):
    try:
        with open(path) as f:
            return f.read().strip()
    except OSError:
        return ""


def proc_info(pid):
    """(comm, start seconds since boot, cmdline) or None."""
    stat = read(f"/proc/{pid}/stat")
    if not stat:
        return None
    comm = stat[stat.index("(") + 1 : stat.rindex(")")]
    fields = stat[stat.rindex(")") + 2 :].split()
    start = int(fields[19]) / HZ  # field 22 overall
    cmd = read(f"/proc/{pid}/cmdline").replace("\0", " ").strip()
    return comm, start, cmd


def pids():
    return [int(p) for p in os.listdir("/proc") if p.isdigit()]


def nvidia_holders():
    held = set()
    for pid in pids():
        try:
            for fd in os.listdir(f"/proc/{pid}/fd"):
                if os.readlink(f"/proc/{pid}/fd/{fd}") == "/dev/nvidia0":
                    held.add(pid)
                    break
        except OSError:
            continue
    return held


def describe(pid, info=None):
    info = info or proc_info(pid)
    if not info:
        return f"{pid} (gone)"
    comm, _, cmd = info
    return f"{pid} {comm}: {cmd[:160]}"


class Seen:
    """Info on every process noticed, recorded on each poll, so a prober that
    exits before the wake is handled is still named. Kernel threads (no
    cmdline) are left out."""

    def __init__(self):
        self.info = {}
        self.refresh()

    def refresh(self):
        live = set(pids())
        for pid in live - self.info.keys():
            info = proc_info(pid)
            if info:
                self.info[pid] = info
        if len(self.info) > 4 * len(live) + 1000:
            self.info = {p: i for p, i in self.info.items() if p in live}

    def recent(self, since):
        return sorted(
            (p, i) for p, i in self.info.items() if i[1] >= since and i[2]
        )


def log(lines):
    os.makedirs(os.path.dirname(LOG), exist_ok=True)
    stamp = time.strftime("%Y-%m-%d %H:%M:%S")
    with open(LOG, "a") as f:
        for i, line in enumerate(lines):
            f.write(f"{stamp} {line}\n" if i == 0 else f"    {line}\n")


def watch():
    prev = read(STATUS)
    holders = nvidia_holders()
    woke_at = time.monotonic() if prev == "active" else None
    seen = Seen()
    log([f"start: dGPU {prev}"])
    while True:
        time.sleep(POLL)
        seen.refresh()
        status = read(STATUS)
        if status == prev:
            continue
        prev = status
        if status == "active":
            woke_at = time.monotonic()
            uptime = float(read("/proc/uptime").split()[0])
            recent = seen.recent(uptime - WINDOW - POLL)
            now = nvidia_holders()
            lines = ["WAKE"]
            lines += [f"started: {describe(p, i)}" for p, i in recent]
            lines += [f"opened /dev/nvidia0: {describe(p)}" for p in sorted(now - holders)]
            if len(lines) == 1:
                lines.append("no new process (an already-running one probed the GPU)")
            holders = now
            log(lines)
        elif status == "suspended":
            awake = f" after {time.monotonic() - woke_at:.1f}s awake" if woke_at else ""
            log([f"suspend{awake}"])
            woke_at = None


def summary():
    wakes = 0
    counts = collections.Counter()
    pat = re.compile(r"^\s+(?:started|opened /dev/nvidia0): \d+ ([^:]+):")
    seen = set()
    with open(LOG) as f:
        for line in f:
            if line.split()[2:3] == ["WAKE"]:
                wakes += 1
                seen = set()
            m = pat.match(line)
            if m and m.group(1) not in seen:
                seen.add(m.group(1))
                counts[m.group(1)] += 1
    print(f"{wakes} wakes")
    for name, n in counts.most_common(25):
        print(f"{n:5}  {name}")


if __name__ == "__main__":
    if sys.argv[1:] == ["--summary"]:
        summary()
    else:
        watch()
