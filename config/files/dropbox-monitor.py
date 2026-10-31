#!/usr/bin/env python3
"""dropbox-monitor — verify the Dropbox daemon is alive and actually syncing.

Run by dropbox-monitor.timer every 5 minutes. A daemon can be "running" while
doing nothing (Oct 2026: 9 days frozen, 2m40s of CPU), so a process check is not
enough. Each run checks, in order:

  1. the daemon process exists (~/.dropbox/dropbox.pid)
  2. it answers on its command socket within SOCKET_TIMEOUT (a hung daemon won't)
  3. its status is not a hard error (paused, can't sync, signed out, full...)
     and it has reached "Up to date" within STUCK_AFTER of observed time
  4. canary: once an hour a small file is written to <Dropbox>/.dropbox-monitor/
     <host>; while the daemon claims "Up to date", that file must also be
     "up to date" within CANARY_TIMEOUT — proves it still notices local changes

A problem that persists for GRACE raises the alert:
  - a critical desktop notification, repeated at most every NOTIFY_EVERY
  - ~/.local/state/dropbox-monitor/alert, printed on SSH login by --motd
On recovery the alert file is removed and one "syncing again" notice is sent.

Usage:
  dropbox-monitor           one check cycle (what the timer runs)
  dropbox-monitor --motd    read-only live check; prints a banner only if unhealthy
  dropbox-monitor --status  read-only live check; always prints, exit 1 if unhealthy
"""

import json
import os
import socket
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

INTERVAL = 5 * 60            # timer period; gaps much longer than this = suspend
GRACE = 10 * 60              # a problem must persist this long before alerting
NOTIFY_EVERY = 30 * 60       # min seconds between repeated desktop notifications
STUCK_AFTER = 6 * 3600       # observed time without "Up to date" before alerting
CANARY_EVERY = 3600          # rewrite the canary file this often
CANARY_TIMEOUT = 15 * 60     # canary must sync this soon while status is Up to date
SOCKET_TIMEOUT = 15

HOME = Path.home()
DROPBOX_DIR = HOME / ".dropbox"
STATE_DIR = Path(os.environ.get("XDG_STATE_HOME", HOME / ".local/state")) / "dropbox-monitor"
STATE_FILE = STATE_DIR / "state.json"
ALERT_FILE = STATE_DIR / "alert"
HOST = socket.gethostname()

# Substrings (lowercase) of daemon status lines that mean sync is not happening.
HARD_ERRORS = ("paused", "can't", "cannot", "isn't running", "not running",
               "sign in", "signed out", "waiting to be linked", "full", "error")


def fmt_time(ts):
    return datetime.fromtimestamp(ts).strftime("%b %d %H:%M")


def fmt_age(seconds):
    seconds = int(seconds)
    if seconds < 3600:
        return f"{seconds // 60}m"
    if seconds < 86400:
        return f"{seconds // 3600}h{seconds % 3600 // 60:02d}m"
    return f"{seconds // 86400}d{seconds % 86400 // 3600}h"


def dropbox_root():
    try:
        info = json.loads((DROPBOX_DIR / "info.json").read_text())
        return Path(info["personal"]["path"])
    except (OSError, ValueError, KeyError):
        return HOME / "Dropbox"


def daemon_pid():
    try:
        pid = int((DROPBOX_DIR / "dropbox.pid").read_text().strip())
        comm = Path(f"/proc/{pid}/comm").read_text().strip()
    except (OSError, ValueError):
        return None
    return pid if "dropbox" in comm else None


def query(command, args=None, timeout=SOCKET_TIMEOUT):
    """Send one command over ~/.dropbox/command_socket; return {key: [values]}."""
    with socket.socket(socket.AF_UNIX) as s:
        s.settimeout(timeout)
        s.connect(str(DROPBOX_DIR / "command_socket"))
        f = s.makefile("rw")
        body = "".join(f"{k}\t{v}\n" for k, v in (args or {}).items())
        f.write(f"{command}\n{body}done\n")
        f.flush()
        lines = []
        while True:
            line = f.readline()
            if not line:
                raise ConnectionError("socket closed mid-reply")
            line = line.rstrip("\n")
            if line == "done":
                break
            lines.append(line)
    if not lines or lines[0] != "ok":
        raise ConnectionError(f"unexpected reply: {lines[:2]}")
    reply = {}
    for line in lines[1:]:
        key, *values = line.split("\t")
        reply[key] = values
    return reply


def load_state():
    try:
        return json.loads(STATE_FILE.read_text())
    except (OSError, ValueError):
        return {}


def save_state(state):
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    tmp = STATE_FILE.with_suffix(".tmp")
    tmp.write_text(json.dumps(state, indent=2))
    tmp.replace(STATE_FILE)


def check(state, now, write, timeout=SOCKET_TIMEOUT):
    """Return (problems, status_line). Updates state in place; writes the
    canary file only when write=True."""
    if not daemon_pid():
        return ["Dropbox daemon is not running"], None

    try:
        status = query("get_dropbox_status", timeout=timeout).get("status", ["?"])
    except (OSError, ConnectionError) as e:
        return [f"Dropbox daemon is not responding ({e})"], None
    status_line = " | ".join(status)
    headline = status[0].lower()

    problems = []
    if headline.startswith("up to date"):
        state["last_up_to_date"] = now
    elif any(err in headline for err in HARD_ERRORS):
        problems.append(f"Dropbox reports: {status_line}")
    else:
        since = state.setdefault("last_up_to_date", now)
        if now - since > STUCK_AFTER:
            problems.append(f"not up to date for {fmt_age(now - since)} "
                            f"(status: {status_line})")

    canary = dropbox_root() / ".dropbox-monitor" / HOST
    # Oct 2026: the canary folder landed in selective sync's ignore set, so the
    # daemon renamed it to ".dropbox-monitor (Selective Sync Conflict N)" each
    # hour and every canary "never synced". The folder is ours: re-include it.
    try:
        ignored = query("get_ignore_set", timeout=timeout).get("ignore_set", [])
    except (OSError, ConnectionError):
        ignored = []
    if str(canary.parent) in ignored:
        if not write:
            problems.append(f"test folder {canary.parent} is excluded by selective "
                            f"sync — the next timer run re-includes it")
            return problems, status_line
        try:
            query("ignore_set_remove", {"paths": canary.parent}, timeout=timeout)
            print(f"re-included {canary.parent} in selective sync")
        except (OSError, ConnectionError) as e:
            problems.append(f"test folder {canary.parent} is excluded by selective "
                            f"sync and re-including it failed: {e}")
            return problems, status_line
        state["canary_written"] = None
    written = state.get("canary_written")
    if write and (written is None or now - written > CANARY_EVERY):
        try:
            canary.parent.mkdir(parents=True, exist_ok=True)
            canary.write_text(f"{HOST} {datetime.fromtimestamp(now).isoformat()}\n")
            state["canary_written"], state["canary_synced"] = now, None
            written = now
        except OSError as e:
            problems.append(f"cannot write canary file {canary}: {e}")

    if written is not None and state.get("canary_synced") is None:
        try:
            fstatus = query("icon_overlay_file_status", {"path": canary},
                            timeout=timeout).get("status", ["?"])[0]
        except (OSError, ConnectionError) as e:
            fstatus = f"query failed: {e}"
        if fstatus == "up to date":
            state["canary_synced"] = now
        elif headline.startswith("up to date") and now - written > CANARY_TIMEOUT:
            problems.append(f"test file written {fmt_time(written)} never synced "
                            f"(file status: {fstatus}) although Dropbox claims "
                            f"Up to date — daemon is not seeing changes")
    return problems, status_line


def notify(summary, body, urgency):
    try:
        subprocess.run(["notify-send", "-a", "Dropbox monitor", "-i", "dropbox",
                        "-u", urgency, summary, body], timeout=10, check=False)
    except (OSError, subprocess.TimeoutExpired) as e:
        print(f"notify-send failed: {e}", file=sys.stderr)


def run_cycle():
    state = load_state()
    now = time.time()

    # Suspend/shutdown gap: don't count unobserved time toward the stuck and
    # canary timers, and restart the grace period.
    last_run = state.get("last_run")
    if last_run and now - last_run > 4 * INTERVAL:
        gap = now - last_run
        for key in ("last_up_to_date", "canary_written"):
            if state.get(key):
                state[key] += gap
        state["first_bad"] = None
    state["last_run"] = now

    problems, status_line = check(state, now, write=True)
    print(f"status: {status_line or '-'}; problems: {problems or 'none'}")

    if problems:
        first_bad = state.get("first_bad") or now
        state["first_bad"] = first_bad
        if now - first_bad >= GRACE:
            STATE_DIR.mkdir(parents=True, exist_ok=True)
            ALERT_FILE.write_text(json.dumps({"since": first_bad, "checked": now,
                                              "problems": problems}))
            last = state.get("last_notified")
            if last is None or now - last >= NOTIFY_EVERY:
                notify(f"Dropbox is NOT syncing (since {fmt_time(first_bad)})",
                       "\n".join(problems), "critical")
                state["last_notified"] = now
    else:
        if ALERT_FILE.exists():
            ALERT_FILE.unlink()
            notify("Dropbox is syncing again",
                   f"Recovered after {fmt_age(now - state['first_bad'])}", "normal")
        state["first_bad"] = state["last_notified"] = None

    save_state(state)


def live_report(always):
    """Read-only check for interactive use (SSH login, manual). The timer may not
    be running at all — no session, no user manager — so check live as well as
    reading the timer's alert."""
    state = load_state()
    now = time.time()
    problems, status_line = check(state, now, write=False, timeout=3)
    since = now
    try:
        alert = json.loads(ALERT_FILE.read_text())
        since = alert["since"]
        # The timer's verdict still stands if it ran recently.
        if now - alert["checked"] < 3 * INTERVAL:
            problems += [p for p in alert["problems"] if p not in problems]
    except (OSError, ValueError, KeyError):
        pass

    if problems:
        red, bold, reset = "\033[1;31m", "\033[1m", "\033[0m"
        print(f"{red}!! DROPBOX IS NOT SYNCING{reset}{bold} — since {fmt_time(since)} "
              f"({fmt_age(now - since)} ago){reset}")
        for p in problems:
            print(f"   - {p}")
        print("   restart:  pkill -x dropbox; systemctl --user restart dropbox")
        print("   history:  journalctl --user -u dropbox-monitor")
        return 1
    if always:
        print(f"Dropbox OK: {status_line}")
    return 0


def main():
    arg = sys.argv[1] if len(sys.argv) > 1 else ""
    if arg == "--motd":
        live_report(always=False)
        return 0
    if arg == "--status":
        return live_report(always=True)
    if arg:
        print(__doc__)
        return 2
    run_cycle()
    return 0


if __name__ == "__main__":
    sys.exit(main())
