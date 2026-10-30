#!/usr/bin/env python3
"""Managed by stay-go (config/files/lg-kbd-daemon.py).

LG Gram (16T90SP) keyboard backlight keepalive. The Embedded Controller cuts
LED power after a fixed idle period unless the level is re-asserted through
the WMI method (Windows does that while you type; Linux never does). This
re-sends the current level every few seconds and, because the same call wakes
the LEDs, right after any key press, so the keys are never dark under your
fingers. It never chooses a level itself: it re-applies whatever the firmware
says was last set, so lg-kbd-backlight off/toggle still win.

ACPI protocol: see lg-kbd-backlight.sh.
"""

import glob
import select
import sys
import time

from evdev import InputDevice

ACPI_NODE = "/proc/acpi/call"
WMAB_GET = r"\_SB.MAP1.WMAB 0x0400 0x01 0"
WMAB_SET = r"\_SB.MAP1.WMAB 0x0400 0x02 "
ON_LEVELS = ("0xa2", "0x80")  # low, high; 0xa4 is off

TICKLE_INTERVAL = 3.0   # seconds; comfortably inside the EC idle timeout
KEYPRESS_MIN_GAP = 0.5  # don't hammer the EC on every keystroke while typing
RESCAN_INTERVAL = 30.0  # notice keyboards that (re)appear, e.g. after a keyd restart


def log(msg):
    print(msg, file=sys.stderr, flush=True)


def acpi_call(cmd):
    with open(ACPI_NODE, "w") as f:
        f.write(cmd)
    with open(ACPI_NODE) as f:
        return f.read().rstrip("\0").strip().lower()


def reassert():
    level = acpi_call(WMAB_GET)
    if level in ON_LEVELS:
        acpi_call(WMAB_SET + level)


def rescan(poller, devs):
    """Start watching any keyboard-like input device not already watched."""
    watched = {dev.path for dev in devs.values()}
    for path in sorted(glob.glob("/dev/input/event*")):
        if path in watched:
            continue
        try:
            dev = InputDevice(path)
        except OSError:
            continue
        if "keyboard" in dev.name.lower():
            devs[dev.fd] = dev
            poller.register(dev.fd, select.EPOLLIN)
            log(f"watching {dev.name} ({path})")
        else:
            dev.close()


def drop(poller, devs, fd):
    dev = devs.pop(fd)
    try:
        poller.unregister(fd)
    except OSError:
        pass
    log(f"lost {dev.name} ({dev.path})")
    dev.close()


def main():
    poller = select.epoll()
    devs = {}
    last_tickle = last_scan = float("-inf")
    while True:
        now = time.monotonic()
        if now - last_scan >= RESCAN_INTERVAL:
            rescan(poller, devs)
            last_scan = now
        if now - last_tickle >= TICKLE_INTERVAL:
            reassert()
            last_tickle = now
        events = poller.poll(max(0.0, TICKLE_INTERVAL - (time.monotonic() - last_tickle)))
        if not events:
            continue
        for fd, _ in events:
            if fd not in devs:
                continue
            try:
                for _ in devs[fd].read():  # drain; any key activity counts
                    pass
            except BlockingIOError:
                pass
            except OSError:
                drop(poller, devs, fd)  # unplugged, or keyd re-created its device
        if time.monotonic() - last_tickle >= KEYPRESS_MIN_GAP:
            reassert()
            last_tickle = time.monotonic()


if __name__ == "__main__":
    main()
