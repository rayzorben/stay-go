#!/usr/bin/env python3
"""
media_organizer.py
==================

A production-grade daemon that recursively watches a directory for media
assets, injects tracking EXIF/XMP metadata, and renames/organizes the files
according to a YAML configuration with a strict metadata-fallback pipeline.

Architecture
------------
    watchdog (inotify)  ->  thread-safe queue  ->  single worker thread
                                                        |
                                                        v
                                    stability check -> metadata guardrails ->
                                    token extraction (fallback pipeline) ->
                                    atomic rename/move (+ collision handling)

Design notes
------------
* The OS-level ``inotify`` observer runs in its own thread and does nothing
  but enqueue candidate paths.  A *single* worker thread drains the queue and
  performs all metadata / filesystem work.  Serializing the heavy work keeps a
  single long-lived ExifTool subprocess busy (fast) and removes the need for
  locking around the ExifTool pipe or around directory creation.
* Files are only touched once they are "stable" (size + mtime unchanged for a
  number of consecutive polls), which avoids the classic race where a large
  RAW file is still being flushed by the camera / rsync / download client.
* Idempotency is enforced by a custom XMP tag.  Because our own metadata write
  and our own move both generate fresh inotify events, the guard tag is what
  stops the daemon from chasing its own tail.

Custom XMP namespaces
---------------------
ExifTool cannot *write* an arbitrary namespaced tag such as
``XMP-custom:AutomationProcessed`` unless that namespace is registered through
an ExifTool config file.  This script will auto-generate a matching
``.ExifTool_config`` on first run (unless you point it at your own via
``exiftool_config`` in the YAML) so the custom tags in the config "just work".

Dependencies
------------
    pip install watchdog PyYAML PyExifTool
    # plus the native ExifTool binary on PATH:
    #   Debian/Ubuntu:  apt install libimage-exiftool-perl
    #   Arch:           pacman -S perl-image-exiftool
"""

from __future__ import annotations

import argparse
import calendar
import logging
import logging.handlers
import os
import queue
import re
import shutil
import signal
import sys
import threading
import time
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Iterable, Optional

import yaml

try:
    # Modern pyexiftool exposes ExifToolHelper (dict-friendly get/set).
    from exiftool import ExifToolHelper
    from exiftool.exceptions import ExifToolExecuteError
except ImportError as exc:  # pragma: no cover - import guard
    print(
        "FATAL: PyExifTool is required (pip install PyExifTool) and the native "
        "`exiftool` binary must be on PATH.\n"
        f"Import error: {exc}",
        file=sys.stderr,
    )
    raise

from watchdog.events import FileSystemEvent, FileSystemEventHandler
from watchdog.observers import Observer


LOG = logging.getLogger("media_organizer")


# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
# Built-in defaults used by the config-less one-shot mode (`-s`).  These mirror
# config.example.yaml so `-s` behaves the same as a minimal config file.
DEFAULT_RENAMING_PATTERN = "{year}/{month}/{year}-{month}-{day}[_{description}]_{original_name}.{ext}"
DEFAULT_FALLBACK_DESCRIPTION = ""
DEFAULT_FILENAME_PARSERS = [
    r"^(?P<year>\d{4})-(?P<month>\d{2})-(?P<day>\d{2})_(?P<description>.+)$",
    r"^(?P<year>\d{4})(?P<month>\d{2})(?P<day>\d{2})_(?P<description>.+)$",
]
DEFAULT_EXTENSIONS = {".jpg", ".jpeg", ".png", ".tif", ".tiff", ".cr2", ".nef"}


@dataclass
class Config:
    """Typed, validated view over the YAML configuration document."""

    # storage:
    watch_directory: Path
    target_directory: Path
    error_directory: Path

    # metadata_tracking:
    use_custom_tag: bool
    custom_tag_namespace: str
    custom_tag_name: str
    original_name_tag: str

    # organization:
    renaming_pattern: str
    fallback_description: str
    filename_parsers: list[re.Pattern] = field(default_factory=list)

    # extensions: (stored normalized: lower-case, leading dot)
    extensions: set[str] = field(default_factory=set)

    # runtime knobs (optional, sane defaults if absent from YAML)
    log_file: Path = Path("media_organizer.log")
    exiftool_config: Optional[Path] = None
    stability_checks: int = 3
    stability_interval: float = 1.0
    stability_timeout: float = 300.0
    stability_min_age: float = 10.0
    process_existing_on_start: bool = True

    # Derived convenience field.
    @property
    def custom_tag_full(self) -> str:
        """e.g. ``XMP-custom:AutomationProcessed``."""
        return f"{self.custom_tag_namespace}:{self.custom_tag_name}"

    @classmethod
    def load(cls, path: Path) -> "Config":
        with open(path, "r", encoding="utf-8") as fh:
            raw = yaml.safe_load(fh) or {}

        def section(name: str) -> dict:
            value = raw.get(name)
            if not isinstance(value, dict):
                raise ValueError(f"Config section '{name}' is missing or malformed")
            return value

        storage = section("storage")
        tracking = section("metadata_tracking")
        organization = section("organization")

        # Compile regex parsers eagerly so a bad pattern fails fast at startup
        # rather than at the first file event.
        compiled: list[re.Pattern] = []
        for pattern in organization.get("filename_parsers", []) or []:
            try:
                compiled.append(re.compile(pattern))
            except re.error as err:
                raise ValueError(f"Invalid filename_parser regex {pattern!r}: {err}")

        # Normalize extensions -> {'.jpg', '.nef', ...} lower-case with dot.
        exts = set()
        for ext in raw.get("extensions", []) or []:
            ext = str(ext).strip().lower()
            if not ext.startswith("."):
                ext = "." + ext
            exts.add(ext)

        runtime = raw.get("runtime", {}) or {}

        cfg = cls(
            watch_directory=Path(storage["watch_directory"]).expanduser().resolve(),
            target_directory=Path(storage["target_directory"]).expanduser().resolve(),
            error_directory=Path(storage["error_directory"]).expanduser().resolve(),
            use_custom_tag=bool(tracking.get("use_custom_tag", True)),
            custom_tag_namespace=str(tracking.get("custom_tag_namespace", "XMP-custom")),
            custom_tag_name=str(tracking.get("custom_tag_name", "AutomationProcessed")),
            original_name_tag=str(tracking.get("original_name_tag", "XMP:OriginalFileName")),
            renaming_pattern=str(organization["default_renaming_pattern"]),
            fallback_description=str(organization.get("fallback_description", "")),
            filename_parsers=compiled,
            extensions=exts,
            log_file=Path(runtime.get("log_file", "media_organizer.log")).expanduser(),
            exiftool_config=(
                Path(runtime["exiftool_config"]).expanduser().resolve()
                if runtime.get("exiftool_config")
                else None
            ),
            stability_checks=int(runtime.get("stability_checks", 3)),
            stability_interval=float(runtime.get("stability_interval", 1.0)),
            stability_timeout=float(runtime.get("stability_timeout", 300.0)),
            stability_min_age=float(runtime.get("stability_min_age", 10.0)),
            process_existing_on_start=bool(runtime.get("process_existing_on_start", True)),
        )
        cfg._validate()
        return cfg

    @classmethod
    def defaults(cls, source: Path, target: Optional[Path] = None) -> "Config":
        """Build a config from built-in defaults (no YAML) for one-shot mode."""
        source = Path(source).expanduser().resolve()
        target = Path(target).expanduser().resolve() if target else source
        return cls(
            watch_directory=source,
            target_directory=target,
            error_directory=target / "_errors",
            use_custom_tag=True,
            custom_tag_namespace="XMP-custom",
            custom_tag_name="AutomationProcessed",
            original_name_tag="XMP:OriginalFileName",
            renaming_pattern=DEFAULT_RENAMING_PATTERN,
            fallback_description=DEFAULT_FALLBACK_DESCRIPTION,
            filename_parsers=[re.compile(p) for p in DEFAULT_FILENAME_PARSERS],
            extensions=set(DEFAULT_EXTENSIONS),
            log_file=Path("media_organizer.log"),
        )

    def _validate(self) -> None:
        if not self.watch_directory.is_dir():
            raise ValueError(f"watch_directory does not exist: {self.watch_directory}")
        if not self.extensions:
            raise ValueError("At least one extension must be configured")
        # Target/error dirs are created lazily; just make sure they are creatable.
        for d in (self.target_directory, self.error_directory):
            d.mkdir(parents=True, exist_ok=True)


# ---------------------------------------------------------------------------
# ExifTool custom-namespace config generation
# ---------------------------------------------------------------------------
def ensure_exiftool_config(cfg: Config, scratch_dir: Path) -> Optional[Path]:
    """
    Return a path to an ExifTool ``-config`` file that registers the custom
    namespace used for our tracking tags, generating one if necessary.

    If the user supplied their own config we trust it and return it as-is.
    If custom tagging is disabled and the original-name tag lives in a standard
    group, no config is needed and we return ``None``.
    """
    if cfg.exiftool_config:
        return cfg.exiftool_config

    # Determine which namespaces we must register.  A tag like
    # "XMP-custom:AutomationProcessed" needs the "custom" namespace registered;
    # a standard tag like "XMP:OriginalFileName" also benefits from a flexible
    # user-defined slot so ExifTool will happily write it.
    #
    # We register everything under a single user-defined XMP namespace whose
    # prefix is derived from the configured namespace ("XMP-custom" -> "custom").
    prefix = cfg.custom_tag_namespace
    if prefix.upper().startswith("XMP-"):
        prefix = prefix[4:]
    prefix = re.sub(r"[^A-Za-z0-9_]", "", prefix) or "custom"

    # Collect the flat tag names we intend to write into this namespace.
    tag_names = {cfg.custom_tag_name}
    orig_group, _, orig_name = cfg.original_name_tag.partition(":")
    orig_name = orig_name or orig_group  # tolerate a bare tag name
    # Only fold the original-name tag into our namespace when it is not a
    # recognised standard XMP group (otherwise leave it to ExifTool's builtins).
    if not orig_group or orig_group.lower() in {"xmp", cfg.custom_tag_namespace.lower()}:
        tag_names.add(orig_name)

    tag_block = "\n".join(f"    {name} => {{ }}," for name in sorted(tag_names))

    config_body = f"""#------------------------------------------------------------------------------
# Auto-generated ExifTool config for media_organizer.py
# Registers the '{prefix}' XMP namespace so custom tracking tags are writable.
#------------------------------------------------------------------------------
%Image::ExifTool::UserDefined = (
    'Image::ExifTool::XMP::Main' => {{
        {prefix} => {{
            SubDirectory => {{
                TagTable => 'Image::ExifTool::UserDefined::{prefix}',
            }},
        }},
    }},
);

%Image::ExifTool::UserDefined::{prefix} = (
    GROUPS    => {{ 0 => 'XMP', 1 => 'XMP-{prefix}', 2 => 'Image' }},
    NAMESPACE => {{ '{prefix}' => 'http://ns.media-organizer.local/{prefix}/1.0/' }},
    WRITABLE  => 'string',
{tag_block}
);

1;  #end
"""

    scratch_dir.mkdir(parents=True, exist_ok=True)
    config_path = scratch_dir / ".ExifTool_config"
    # Only rewrite when content changed, so we don't needlessly touch mtimes.
    if not config_path.exists() or config_path.read_text(encoding="utf-8") != config_body:
        config_path.write_text(config_body, encoding="utf-8")
        LOG.info("Wrote ExifTool custom-namespace config to %s", config_path)
    return config_path


# ---------------------------------------------------------------------------
# Metadata service (thin wrapper over a persistent ExifTool process)
# ---------------------------------------------------------------------------
class MetadataService:
    """
    Owns a single long-lived ExifTool subprocess.  All access is expected to
    happen from the single worker thread, so no locking is required here.
    """

    # Candidate tags to read for date extraction, in priority order.
    _DATE_TAGS = (
        "EXIF:DateTimeOriginal",
        "EXIF:CreateDate",
        "QuickTime:CreateDate",
        "XMP:DateTimeOriginal",
        "Composite:SubSecDateTimeOriginal",
    )

    def __init__(self, cfg: Config, config_file: Optional[Path]) -> None:
        self._cfg = cfg
        # -G  : return group-qualified keys (EXIF:DateTimeOriginal, ...)
        # -n  : disable print conversion (raw, machine-parseable values)
        # -api QuickTimeUTC : QuickTime:CreateDate is stored as UTC; convert
        #       it to local time, or evening videos land on the next day
        common_args = ["-G", "-n", "-api", "QuickTimeUTC"]
        kwargs = {"common_args": common_args}
        if config_file is not None:
            kwargs["config_file"] = str(config_file)
        self._et = ExifToolHelper(**kwargs)

    # -- lifecycle ---------------------------------------------------------
    def start(self) -> None:
        if not self._et.running:
            self._et.run()

    def close(self) -> None:
        try:
            if self._et.running:
                self._et.terminate()
        except Exception:  # pragma: no cover - best effort on shutdown
            LOG.debug("ExifTool terminate raised on shutdown", exc_info=True)

    # -- reads -------------------------------------------------------------
    def read(self, path: Path, tags: Optional[Iterable[str]] = None) -> dict:
        """Return the metadata dict for *path* (empty dict on failure)."""
        try:
            result = self._et.get_tags([str(path)], tags=list(tags) if tags else None)
        except ExifToolExecuteError as err:
            LOG.warning("ExifTool read failed for %s: %s", path, err.stderr or err)
            return {}
        return result[0] if result else {}

    def is_already_processed(self, meta: dict) -> bool:
        """True when our idempotency guard tag is present and truthy."""
        if not self._cfg.use_custom_tag:
            return False
        val = _first_value(meta, self._cfg.custom_tag_full, self._cfg.custom_tag_name)
        return val not in (None, "", 0, "0", False)

    def has_original_name(self, meta: dict) -> bool:
        _, _, bare = self._cfg.original_name_tag.partition(":")
        val = _first_value(meta, self._cfg.original_name_tag, bare or self._cfg.original_name_tag)
        return val not in (None, "")

    def extract_datetime(self, meta: dict) -> Optional[datetime]:
        """Best-effort parse of an EXIF/XMP creation timestamp."""
        for tag in self._DATE_TAGS:
            raw = meta.get(tag)
            if raw:
                parsed = _parse_exif_datetime(str(raw))
                if parsed:
                    return parsed
        return None

    def date_tag(self, meta: dict) -> tuple[Optional[str], Optional[str]]:
        """Return (tag_name, raw_value) of the first present date tag, else (None, None)."""
        for tag in self._DATE_TAGS:
            if meta.get(tag):
                return tag, str(meta[tag])
        return None, None

    def camera_tokens(self, meta: dict) -> dict[str, str]:
        make = _first_value(meta, "EXIF:Make", "MakerNotes:Make") or ""
        model = _first_value(meta, "EXIF:Model", "MakerNotes:Model") or ""
        return {
            "camera_make": _sanitize_token(str(make)) or "unknown",
            "camera_model": _sanitize_token(str(model)) or "unknown",
        }

    # -- writes ------------------------------------------------------------
    def write_tracking_tags(self, path: Path, original_filename: str, write_original: bool) -> None:
        """
        Write the idempotency guard and (optionally) the original-filename tag.

        *write_original* is False when the file already carries an
        OriginalFileName tag — we must never clobber real provenance.
        """
        tags: dict[str, str] = {}
        if self._cfg.use_custom_tag:
            # Store an ISO timestamp so the tag doubles as an audit trail.
            tags[self._cfg.custom_tag_full] = datetime.now().strftime("%Y-%m-%dT%H:%M:%S")
        if write_original:
            tags[self._cfg.original_name_tag] = original_filename

        if not tags:
            return

        try:
            # -overwrite_original: don't leave *_original sidecar copies behind.
            self._et.set_tags([str(path)], tags=tags, params=["-overwrite_original"])
        except ExifToolExecuteError as err:
            # Writing metadata is important but must not abort the whole
            # pipeline: many formats (e.g. some PNG/RAW variants) may reject a
            # given tag.  Surface it loudly and continue with organization.
            LOG.error(
                "Failed to write tracking tags to %s: %s",
                path,
                err.stderr or err,
            )
            raise


# ---------------------------------------------------------------------------
# Token extraction (the fallback pipeline)
# ---------------------------------------------------------------------------
@dataclass
class Tokens:
    year: str
    month: str
    day: str
    hour: str
    minute: str
    second: str
    description: str
    original_name: str
    ext: str
    source: str  # "exif" | "filename" | "mtime" — for logging/audit
    extra: dict[str, str] = field(default_factory=dict)

    def as_mapping(self) -> dict[str, str]:
        base = {
            "year": self.year,
            "month": self.month,
            "day": self.day,
            "hour": self.hour,
            "minute": self.minute,
            "second": self.second,
            "description": self.description,
            "original_name": self.original_name,
            "ext": self.ext,
        }
        base.update(self.extra)
        return base


# --- Robust date parsing --------------------------------------------------
#
# Ported and hardened from the original Node "tagger.js": scan the filename
# AND the directory names, try an ordered table of numeric formats (most
# specific first), then fall back to spelled-out month names.  Every candidate
# is validated (real calendar day, plausible year window) before it is trusted.

# A "plausible photo year": 1920..2049 at the regex level; the real upper bound
# is clamped to the current year at validation time so a stray "2049" folder
# can't win.
_YEAR = r"(?:19[2-9]\d|20[0-4]\d)"
_MIN_YEAR = 1920

# Ordered most-specific -> least-specific.  Named groups:
# year / year2 (2-digit) / month / day / hour / minute / second.
# (?<!\d)/(?!\d) fences stop a date from being carved out of a longer number.
_NUMERIC_FORMATS: list[tuple[str, re.Pattern, str]] = [
    # 2015-06-20 14.30.45   /   2015/06/20T14:30:45
    ("datetime_sep", re.compile(
        rf"(?<!\d)(?P<year>{_YEAR})[-/.](?P<month>0?[1-9]|1[0-2])[-/.]"
        rf"(?P<day>0?[1-9]|[12]\d|3[01])[ T_]+"
        rf"(?P<hour>[01]\d|2[0-3])[.:_](?P<minute>[0-5]\d)[.:_](?P<second>[0-5]\d)"), "second"),
    # 20150620_143045  /  20150620143045  /  20150620_14_30_45
    ("datetime_compact", re.compile(
        rf"(?<!\d)(?P<year>{_YEAR})(?P<month>0[1-9]|1[0-2])(?P<day>0[1-9]|[12]\d|3[01])"
        rf"[ T_]?(?P<hour>[01]\d|2[0-3])[.:_]?(?P<minute>[0-5]\d)[.:_]?(?P<second>[0-5]\d)(?!\d)"), "second"),
    # 2015-06-20  /  2015.06.20  /  2015/6/2
    ("date_iso", re.compile(
        rf"(?<!\d)(?P<year>{_YEAR})[-/.](?P<month>0?[1-9]|1[0-2])[-/.]"
        rf"(?P<day>0?[1-9]|[12]\d|3[01])(?!\d)"), "day"),
    # 20150620  (bare 8 digits)
    ("date_compact", re.compile(
        rf"(?<!\d)(?P<year>{_YEAR})(?P<month>0[1-9]|1[0-2])(?P<day>0[1-9]|[12]\d|3[01])(?!\d)"), "day"),
    # 06-20-2015  /  6/20/2015
    ("date_mdy", re.compile(
        rf"(?<!\d)(?P<month>0?[1-9]|1[0-2])[-/](?P<day>0?[1-9]|[12]\d|3[01])[-/]"
        rf"(?P<year>{_YEAR})(?!\d)"), "day"),
    # 06-20-15  (2-digit year, resolved with a pivot)
    ("date_mdy2", re.compile(
        r"(?<!\d)(?P<month>0?[1-9]|1[0-2])[-/](?P<day>0?[1-9]|[12]\d|3[01])[-/]"
        r"(?P<year2>\d{2})(?!\d)"), "day"),
    # 2015-06  (year + month, no day)
    ("year_month", re.compile(
        rf"(?<!\d)(?P<year>{_YEAR})[-/.](?P<month>0?[1-9]|1[0-2])(?![-/.]?\d)"), "month"),
    # 06-2015  (month + year)
    ("month_year", re.compile(
        rf"(?<!\d)(?P<month>0?[1-9]|1[0-2])[-/.](?P<year>{_YEAR})(?!\d)"), "month"),
]

# A bare year is the weakest signal, so it is tried only as a last resort
# (after named-month parsing) — otherwise a ".../2015/June/..." path would be
# read as "just 2015" and lose the month.  Still excludes camera sequences
# like IMG_2015 / DSC_2015.
_YEAR_ONLY_FORMAT: tuple[str, re.Pattern, str] = (
    "year_only", re.compile(rf"(?<!DSC_)(?<!IMG_)(?<!\d)(?P<year>{_YEAR})(?!\d)"), "year")

# A looser year matcher used only when a month *name* is present nearby.
_YEAR_LOOSE = re.compile(rf"(?<!\d)(?P<year>{_YEAR})(?!\d)")

# Month names (abbrev + full). Lookarounds instead of \b so "June_2015" and
# "2015_Aug" match (underscore is a word char, which would defeat \b).
_MONTH_NAME_RE = re.compile(
    r"(?<![A-Za-z])(?P<mon>jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|"
    r"jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t)?(?:ember)?|oct(?:ober)?|"
    r"nov(?:ember)?|dec(?:ember)?)(?![A-Za-z])", re.I)
_MONTH_ABBR = {
    "jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
    "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}


@dataclass
class DateResult:
    year: int
    month: int
    day: int
    hour: int
    minute: int
    second: int
    precision: str   # "year" | "month" | "day" | "second"
    origin: str      # "filename" | "directory" | "named"
    matched: str     # the exact substring that matched (for logging)

    def to_datetime(self) -> datetime:
        return datetime(self.year, self.month, self.day, self.hour, self.minute, self.second)


class DateParser:
    """Extract a creation date from a filename and/or its directory names."""

    def parse(self, stem: str, dir_parts: list[str]) -> Optional[DateResult]:
        current_year = datetime.now().year
        sources = [(stem, "filename")] + [(p, "directory") for p in dir_parts]

        # 1) Strong numeric formats (those that carry at least a month) against
        #    the filename first, then each directory name nearest-to-furthest.
        for text, origin in sources:
            found = self._numeric(text, _NUMERIC_FORMATS, origin, current_year)
            if found:
                return found

        # 2) Spelled-out month names (e.g. ".../2015/June/Aug 03.jpg"),
        #    searched across the whole path context.
        found = self._named(" ".join([stem, *dir_parts]), current_year)
        if found:
            return found

        # 3) Last resort: a bare year anywhere in the filename or directories.
        for text, origin in sources:
            found = self._numeric(text, [_YEAR_ONLY_FORMAT], origin, current_year)
            if found:
                return found
        return None

    # -- numeric table ------------------------------------------------------
    def _numeric(self, text: str, formats: list, origin: str, current_year: int) -> Optional[DateResult]:
        for _name, pattern, precision in formats:
            for m in pattern.finditer(text):
                result = self._build(m, precision, origin, current_year)
                if result:
                    return result
        return None

    def _build(self, m: re.Match, precision: str, origin: str, current_year: int) -> Optional[DateResult]:
        gd = m.groupdict()
        if gd.get("year"):
            year = int(gd["year"])
        elif gd.get("year2") is not None:
            year = self._resolve_two_digit_year(gd["year2"], current_year)
        else:
            return None
        if not (_MIN_YEAR <= year <= current_year):
            return None

        month = int(gd["month"]) if gd.get("month") else 1
        day = int(gd["day"]) if gd.get("day") else 1
        hour = int(gd["hour"]) if gd.get("hour") else 0
        minute = int(gd["minute"]) if gd.get("minute") else 0
        second = int(gd["second"]) if gd.get("second") else 0

        if not 1 <= month <= 12:
            month, precision = 1, "year"
        # Reject impossible days (Feb 30, etc.) instead of silently shifting.
        max_day = calendar.monthrange(year, month)[1]
        if not 1 <= day <= max_day:
            day = 1
            if precision in ("day", "second"):
                precision = "month"
        if not 0 <= hour <= 23:
            hour = 0
        if not 0 <= minute <= 59:
            minute = 0
        if not 0 <= second <= 59:
            second = 0

        return DateResult(year, month, day, hour, minute, second, precision, origin, m.group(0))

    @staticmethod
    def _resolve_two_digit_year(value: str, current_year: int) -> int:
        """Pivot a 2-digit year: <= current YY -> 2000s, else 1900s."""
        yy = int(value)
        return 2000 + yy if yy <= current_year % 100 else 1900 + yy

    # -- named months -------------------------------------------------------
    def _named(self, text: str, current_year: int) -> Optional[DateResult]:
        # A month name is only useful if we can also anchor a year.
        year = None
        for m in _YEAR_LOOSE.finditer(text):
            candidate = int(m.group("year"))
            if _MIN_YEAR <= candidate <= current_year:
                year = candidate
                break
        if year is None:
            return None

        mm = _MONTH_NAME_RE.search(text)
        if not mm:
            return None
        month = _MONTH_ABBR[mm.group("mon").lower()[:3]]

        # Look for a day adjacent to the month name, e.g. "Aug 03" / "Aug_3".
        day, precision = 1, "month"
        day_match = re.search(
            re.escape(mm.group("mon")) + r"[\s._,-]*?(\d{1,2})(?!\d)", text, re.I)
        if day_match:
            candidate = int(day_match.group(1))
            if 1 <= candidate <= calendar.monthrange(year, month)[1]:
                day, precision = candidate, "day"

        return DateResult(year, month, day, 0, 0, 0, precision, "named", mm.group(0))


class TokenExtractor:
    """EXIF -> (filename + directory) date parsing -> mtime fallback chain."""

    def __init__(self, cfg: Config, meta_service: MetadataService) -> None:
        self._cfg = cfg
        self._meta = meta_service
        self._dates = DateParser()

    def resolve_date(self, path: Path, meta: dict) -> tuple[datetime, str, str]:
        """
        Run the full fallback chain and return (datetime, origin, precision).

        origin: "exif" | "filename" | "directory" | "named" | "mtime"
        precision: "year" | "month" | "day" | "second"
        """
        # -- Primary source: EXIF/XMP creation date ------------------------
        dt = self._meta.extract_datetime(meta)
        if dt is not None:
            return dt, "exif", "second"

        # -- Fallback 1: parse a date out of the filename / directory names -
        parsed = self._dates.parse(path.stem, self._dir_parts(path))
        if parsed is not None:
            LOG.debug(
                "Parsed date %s from %s (matched %r, %s precision) for %s",
                parsed.to_datetime().isoformat(), parsed.origin, parsed.matched,
                parsed.precision, path.name,
            )
            return parsed.to_datetime(), parsed.origin, parsed.precision

        # -- Fallback 1b: honour any YAML filename_parsers that carry a date -
        yaml_dt = self._date_from_yaml(path.stem)
        if yaml_dt is not None:
            return yaml_dt, "filename", "day"

        # -- Fallback 2: filesystem modification time ---------------------
        return datetime.fromtimestamp(path.stat().st_mtime), "mtime", "second"

    def resolve_description(self, stem: str) -> str:
        description = self._description(stem) or self._cfg.fallback_description
        return _sanitize_token(description) or self._cfg.fallback_description

    def extract(self, path: Path, meta: dict) -> Tokens:
        original_name = _sanitize_token(path.stem) or "file"
        ext = path.suffix.lstrip(".").lower()
        extra = self._meta.camera_tokens(meta)

        dt, source, _precision = self.resolve_date(path, meta)
        description = self.resolve_description(path.stem)

        return Tokens(
            year=f"{dt.year:04d}",
            month=f"{dt.month:02d}",
            day=f"{dt.day:02d}",
            hour=f"{dt.hour:02d}",
            minute=f"{dt.minute:02d}",
            second=f"{dt.second:02d}",
            description=description,
            original_name=original_name,
            ext=ext,
            source=source,
            extra=extra,
        )

    def _dir_parts(self, path: Path) -> list[str]:
        """Directory names between watch_directory and the file, nearest first."""
        try:
            rel = path.resolve().relative_to(self._cfg.watch_directory)
        except (ValueError, OSError):
            return []
        return list(reversed(rel.parent.parts))

    def _description(self, stem: str) -> Optional[str]:
        """First YAML parser that captures a non-empty 'description' group wins."""
        for pattern in self._cfg.filename_parsers:
            m = pattern.match(stem)
            if m and m.groupdict().get("description"):
                return m.group("description")
        return None

    def _date_from_yaml(self, stem: str) -> Optional[datetime]:
        """Structured date from a YAML parser, used only if built-ins found none."""
        for pattern in self._cfg.filename_parsers:
            m = pattern.match(stem)
            if not m:
                continue
            gd = m.groupdict()
            if not (gd.get("year") and gd.get("month") and gd.get("day")):
                continue
            try:
                return datetime(
                    int(gd["year"]), int(gd["month"]), int(gd["day"]),
                    int(gd.get("hour") or 0), int(gd.get("minute") or 0),
                    int(gd.get("second") or 0),
                )
            except ValueError:
                continue
        return None


# ---------------------------------------------------------------------------
# File organizer (path building, collision handling, atomic move)
# ---------------------------------------------------------------------------
class FileOrganizer:
    def __init__(self, cfg: Config) -> None:
        self._cfg = cfg

    def build_target_path(self, tokens: Tokens) -> Path:
        # ``_render_pattern`` expands {tokens} and drops empty [optional
        # sections]; the forgiving mapping means an unknown token in the
        # user's pattern degrades to "unknown" instead of crashing the daemon.
        rel = _render_pattern(self._cfg.renaming_pattern, _SafeDict(tokens.as_mapping()))
        rel = rel.strip("/")
        # Guard against path traversal (".." / absolute) sneaking in via tokens.
        candidate = (self._cfg.target_directory / rel).resolve()
        if not _is_within(candidate, self._cfg.target_directory):
            raise ValueError(f"Refusing to move outside target_directory: {candidate}")
        return candidate

    def move(self, src: Path, dst: Path) -> Path:
        """Atomically move *src* -> a collision-free path near *dst*."""
        dst.parent.mkdir(parents=True, exist_ok=True)
        final = _dedupe_path(dst)
        _atomic_move(src, final)
        return final

    def move_to_error(self, src: Path) -> Optional[Path]:
        """Quarantine a failing asset so it stops blocking the queue."""
        try:
            self._cfg.error_directory.mkdir(parents=True, exist_ok=True)
            dst = _dedupe_path(self._cfg.error_directory / src.name)
            _atomic_move(src, dst)
            return dst
        except Exception:  # pragma: no cover - last-ditch effort
            LOG.exception("Could not move %s to error directory", src)
            return None


# ---------------------------------------------------------------------------
# The processor: orchestrates one file end-to-end
# ---------------------------------------------------------------------------
@dataclass
class FilePlan:
    """A read-only description of what would happen to one file (for --dry-run)."""

    path: Path
    status: str = "plan"          # "plan" | "skip" | "error"
    reason: str = ""

    # what was read from metadata
    date_tag: Optional[str] = None
    date_value: Optional[str] = None
    make: str = ""
    model: str = ""
    custom_present: bool = False
    original_present: bool = False
    original_value: Optional[str] = None

    # what was resolved
    resolved_date: Optional[datetime] = None
    date_origin: str = ""
    date_precision: str = ""
    description: str = ""

    # what would be written / where it would go
    writes: dict[str, str] = field(default_factory=dict)
    target: Optional[Path] = None
    collision: bool = False


class MediaProcessor:
    def __init__(self, cfg: Config, meta: MetadataService) -> None:
        self._cfg = cfg
        self._meta = meta
        self._extractor = TokenExtractor(cfg, meta)
        self._organizer = FileOrganizer(cfg)

    def plan(self, path: Path) -> FilePlan:
        """Compute what ``process`` would do, without any writes or moves."""
        fp = FilePlan(path=path)
        try:
            meta = self._meta.read(path)
        except Exception as err:  # pragma: no cover - defensive
            fp.status, fp.reason = "error", f"could not read metadata: {err}"
            return fp

        # -- what we read -------------------------------------------------
        fp.date_tag, fp.date_value = self._meta.date_tag(meta)
        fp.make = str(_first_value(meta, "EXIF:Make", "MakerNotes:Make") or "")
        fp.model = str(_first_value(meta, "EXIF:Model", "MakerNotes:Model") or "")
        fp.custom_present = self._meta.is_already_processed(meta)
        fp.original_present = self._meta.has_original_name(meta)
        _, _, bare = self._cfg.original_name_tag.partition(":")
        fp.original_value = _first_value(meta, self._cfg.original_name_tag, bare) or None

        # -- idempotency guard -------------------------------------------
        if fp.custom_present:
            fp.status, fp.reason = "skip", "already processed"
            return fp

        # -- what we would write -----------------------------------------
        if self._cfg.use_custom_tag:
            fp.writes[self._cfg.custom_tag_full] = "<timestamp>"
        if not fp.original_present:
            fp.writes[self._cfg.original_name_tag] = path.name

        # -- resolved tokens + destination -------------------------------
        try:
            fp.resolved_date, fp.date_origin, fp.date_precision = self._extractor.resolve_date(path, meta)
            fp.description = self._extractor.resolve_description(path.stem)
            tokens = self._extractor.extract(path, meta)
            target = self._organizer.build_target_path(tokens)
            final = _dedupe_path(target)
            fp.target = final
            fp.collision = final != target
        except Exception as err:
            fp.status, fp.reason = "error", f"could not build target path: {err}"
        return fp

    def should_consider(self, path: Path) -> bool:
        """Cheap pre-filter applied before any expensive work."""
        if path.suffix.lower() not in self._cfg.extensions:
            return False
        # Never reprocess files that already live in target/error trees
        # (relevant when those directories are nested inside watch_directory).
        if _is_within(path, self._cfg.target_directory) or _is_within(path, self._cfg.error_directory):
            return False
        return True

    def process(self, path: Path) -> None:
        """Full pipeline for a single, already-stable file."""
        if not path.exists():
            LOG.debug("Vanished before processing: %s", path)
            return

        try:
            # Read everything we need in a single ExifTool round-trip.
            meta = self._meta.read(path)

            # -- Guardrail 1: idempotency ----------------------------------
            if self._meta.is_already_processed(meta):
                LOG.info("Skip (already processed): %s", path)
                return

            # -- Guardrail 2: preserve provenance --------------------------
            has_original = self._meta.has_original_name(meta)
            self._meta.write_tracking_tags(
                path,
                original_filename=path.name,
                write_original=not has_original,
            )
            # Re-read so token extraction sees a consistent view; also the
            # metadata write may have refreshed the mtime.
            meta = self._meta.read(path)

            # -- Tokenization / fallback pipeline --------------------------
            tokens = self._extractor.extract(path, meta)

            # -- Rename / sort ---------------------------------------------
            dst = self._organizer.build_target_path(tokens)
            final = self._organizer.move(path, dst)
            LOG.info(
                "Organized %s -> %s (date source: %s)",
                path.name,
                final,
                tokens.source,
            )

        except Exception:
            LOG.exception("Processing failed for %s; quarantining", path)
            moved = self._organizer.move_to_error(path)
            if moved:
                LOG.warning("Moved failing asset to %s", moved)


# ---------------------------------------------------------------------------
# Watchdog integration: enqueue only, do work on a dedicated thread
# ---------------------------------------------------------------------------
class _EnqueueHandler(FileSystemEventHandler):
    """Translate raw filesystem events into de-duplicated work items."""

    def __init__(self, work_queue: "queue.Queue[Path]", processor: MediaProcessor) -> None:
        super().__init__()
        self._q = work_queue
        self._processor = processor

    def _submit(self, raw_path: str) -> None:
        path = Path(raw_path)
        if self._processor.should_consider(path):
            self._q.put(path)

    def on_created(self, event: FileSystemEvent) -> None:
        if not event.is_directory:
            self._submit(event.src_path)

    def on_moved(self, event: FileSystemEvent) -> None:
        # A file moved *into* the tree (or renamed) — the destination is the
        # thing we may need to process.
        if not event.is_directory:
            self._submit(event.dest_path)

    def on_modified(self, event: FileSystemEvent) -> None:
        # Some copy tools create a 0-byte file then stream into it; catching
        # modify events (dedupe + stability check handle the noise) makes us
        # robust to that pattern.
        if not event.is_directory:
            self._submit(event.src_path)


class Worker(threading.Thread):
    """Single consumer thread: stability-gate then hand off to the processor."""

    def __init__(self, cfg: Config, work_queue: "queue.Queue[Path]", processor: MediaProcessor) -> None:
        super().__init__(name="media-worker", daemon=True)
        self._cfg = cfg
        self._q = work_queue
        self._processor = processor
        self._stop = threading.Event()
        # Coalesce duplicate events for the same path that are still pending.
        self._inflight: set[Path] = set()
        self._inflight_lock = threading.Lock()

    def stop(self) -> None:
        self._stop.set()

    def run(self) -> None:
        while not self._stop.is_set():
            try:
                path = self._q.get(timeout=0.5)
            except queue.Empty:
                continue

            with self._inflight_lock:
                if path in self._inflight:
                    self._q.task_done()
                    continue
                self._inflight.add(path)

            try:
                if not self._processor.should_consider(path):
                    continue
                if not _wait_until_stable(
                    path,
                    checks=self._cfg.stability_checks,
                    interval=self._cfg.stability_interval,
                    timeout=self._cfg.stability_timeout,
                    min_age=self._cfg.stability_min_age,
                ):
                    try:
                        size = path.stat().st_size
                    except OSError:
                        size = None
                    if size is None:
                        # Expected noise: our own exiftool write and move fire
                        # inotify events that re-enqueue the path, and by the
                        # time we drain that duplicate the file has already
                        # been organized away.
                        LOG.debug("Stale event for already-moved file, ignoring: %s", path)
                    elif size == 0:
                        LOG.warning("Skipping empty (0-byte) file: %s", path)
                    else:
                        LOG.warning(
                            "File never stabilized within %.0fs (still being written?), skipping: %s",
                            self._cfg.stability_timeout, path,
                        )
                    continue
                self._processor.process(path)
            except Exception:  # pragma: no cover - worker must never die
                LOG.exception("Unexpected worker error for %s", path)
            finally:
                with self._inflight_lock:
                    self._inflight.discard(path)
                self._q.task_done()


# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------
class _SafeDict(dict):
    """dict subclass used with str.format_map: unknown tokens -> 'unknown'."""

    def __missing__(self, key: str) -> str:  # noqa: D401
        LOG.debug("Unknown token {%s} in renaming pattern", key)
        return "unknown"


def _render_pattern(pattern: str, mapping: dict) -> str:
    """
    Expand *pattern* with str.format-style ``{tokens}`` plus optional sections
    in ``[square brackets]`` (the Mp3tag / foobar2000 titleformat convention):
    a bracketed section is emitted only when at least one token inside it
    expands to a non-empty value; otherwise the whole section — separator
    text and all — is dropped.  Sections may nest.  Unmatched brackets are
    treated as literal characters; ``{{``/``}}`` escape literal braces.
    """
    # One frame per open '[': [text, saw_token, any_token_nonempty]
    stack: list[list] = [["", False, False]]
    i, n = 0, len(pattern)
    while i < n:
        ch = pattern[i]
        if ch == "[":
            stack.append(["", False, False])
            i += 1
        elif ch == "]" and len(stack) > 1:
            text, saw, nonempty = stack.pop()
            if saw and not nonempty:
                text = ""
            top = stack[-1]
            top[0] += text
            top[1] = top[1] or saw
            top[2] = top[2] or nonempty
            i += 1
        elif ch == "{" and pattern[i + 1 : i + 2] == "{":
            stack[-1][0] += "{"
            i += 2
        elif ch == "}" and pattern[i + 1 : i + 2] == "}":
            stack[-1][0] += "}"
            i += 2
        elif ch == "{":
            end = pattern.find("}", i)
            if end == -1:
                raise ValueError(f"Unbalanced '{{' in renaming pattern: {pattern!r}")
            value = pattern[i : end + 1].format_map(mapping)
            top = stack[-1]
            top[0] += value
            top[1] = True
            top[2] = top[2] or bool(value)
            i = end + 1
        else:
            stack[-1][0] += ch
            i += 1
    while len(stack) > 1:  # unmatched '[' -> keep it (and its content) literally
        text, saw, nonempty = stack.pop()
        top = stack[-1]
        top[0] += "[" + text
        top[1] = top[1] or saw
        top[2] = top[2] or nonempty
    return stack[0][0]


_INVALID_PATH_CHARS = re.compile(r'[<>:"/\\|?*\x00-\x1f]')
_WHITESPACE = re.compile(r"\s+")


def _sanitize_token(value: str, max_len: int = 100) -> str:
    """Make a token safe to embed in a path component."""
    if value is None:
        return ""
    value = _INVALID_PATH_CHARS.sub("", str(value))
    value = _WHITESPACE.sub("_", value.strip())
    value = value.strip("._-")
    return value[:max_len]


def _first_value(meta: dict, *keys: str):
    """Return the first present, non-None value among *keys* in *meta*."""
    for key in keys:
        if key in meta and meta[key] is not None:
            return meta[key]
    # Fall back to a case/group-insensitive suffix match (ExifTool sometimes
    # returns a different group prefix than requested).
    wanted = {k.split(":")[-1].lower() for k in keys}
    for mk, mv in meta.items():
        if mk.split(":")[-1].lower() in wanted and mv is not None:
            return mv
    return None


def _parse_exif_datetime(raw: str) -> Optional[datetime]:
    """Parse the common EXIF/XMP datetime encodings; None if unparseable."""
    raw = raw.strip()
    # Drop timezone suffix and sub-second fraction for simplicity/robustness.
    raw = re.sub(r"([+-]\d{2}:?\d{2}|Z)$", "", raw).strip()
    raw = re.sub(r"\.\d+", "", raw)
    for fmt in (
        "%Y:%m:%d %H:%M:%S",
        "%Y-%m-%d %H:%M:%S",
        "%Y:%m:%d",
        "%Y-%m-%dT%H:%M:%S",
        "%Y-%m-%d",
    ):
        try:
            return datetime.strptime(raw, fmt)
        except ValueError:
            continue
    return None


def _is_within(path: Path, ancestor: Path) -> bool:
    """True if *path* is *ancestor* or nested under it (resolved)."""
    try:
        path = path.resolve()
        ancestor = ancestor.resolve()
    except OSError:
        # Path may not exist yet; fall back to lexical comparison.
        path = Path(os.path.normpath(str(path)))
        ancestor = Path(os.path.normpath(str(ancestor)))
    return ancestor == path or ancestor in path.parents


def _dedupe_path(dst: Path) -> Path:
    """
    Return a path that does not yet exist, appending _1, _2, ... to the stem
    on collision (e.g. photo.jpg -> photo_1.jpg -> photo_2.jpg).
    """
    if not dst.exists():
        return dst
    stem, suffix, parent = dst.stem, dst.suffix, dst.parent
    n = 1
    while True:
        candidate = parent / f"{stem}_{n}{suffix}"
        if not candidate.exists():
            return candidate
        n += 1


def _atomic_move(src: Path, dst: Path) -> None:
    """
    Move *src* to *dst* as atomically as the filesystem allows.

    Within one filesystem ``os.replace`` is atomic.  Across filesystems that is
    impossible, so we copy to a temp sibling of *dst* and then ``os.replace``
    into place — the final rename is still atomic even if the copy was not.
    """
    try:
        os.replace(src, dst)  # fast path: same filesystem, truly atomic
        return
    except OSError as err:
        import errno

        if err.errno != errno.EXDEV:
            raise
    # Cross-device: stage a copy next to the destination, then atomic-rename.
    tmp = dst.parent / f".{dst.name}.tmp-{os.getpid()}"
    try:
        shutil.copy2(src, tmp)
        os.replace(tmp, dst)
        os.remove(src)
    finally:
        if tmp.exists():
            try:
                tmp.unlink()
            except OSError:
                pass


def _wait_until_stable(path: Path, checks: int, interval: float, timeout: float,
                       min_age: float = 0.0) -> bool:
    """
    Block until *path* has an unchanged (size, mtime) for *checks* consecutive
    polls, indicating the writer is done.  Returns False on timeout/removal.

    Fast path: a file whose mtime is already at least *min_age* seconds old
    cannot still be mid-write (an active writer keeps bumping the mtime), so
    it is accepted — or, when empty, rejected — without any polling.  This is
    what keeps the startup sweep of thousands of pre-existing files from
    paying ``checks * interval`` seconds each.
    """
    if min_age > 0:
        try:
            st = path.stat()
        except FileNotFoundError:
            return False
        if time.time() - st.st_mtime >= min_age:
            if st.st_size == 0:
                return False  # long-abandoned empty file: nothing is coming
            try:
                with open(path, "rb"):
                    pass
                return True
            except OSError:
                pass  # momentarily unreadable: fall back to polling below

    deadline = time.monotonic() + timeout
    last: Optional[tuple[int, float]] = None
    stable = 0
    while time.monotonic() < deadline:
        try:
            st = path.stat()
        except FileNotFoundError:
            return False
        signature = (st.st_size, st.st_mtime)
        # A zero-byte file is almost certainly still being created.
        if st.st_size == 0:
            stable = 0
        elif signature == last:
            stable += 1
            if stable >= checks:
                # Final belt-and-braces: make sure we can open it for reading.
                try:
                    with open(path, "rb"):
                        pass
                    return True
                except OSError:
                    stable = 0
        else:
            stable = 0
        last = signature
        time.sleep(interval)
    return False


def _iter_existing_media(cfg: Config) -> Iterable[Path]:
    """Yield candidate files already present in the watch tree at startup."""
    for root, _dirs, files in os.walk(cfg.watch_directory):
        root_path = Path(root)
        # Prune target/error subtrees.
        if _is_within(root_path, cfg.target_directory) or _is_within(root_path, cfg.error_directory):
            continue
        for name in files:
            p = root_path / name
            if p.suffix.lower() in cfg.extensions:
                yield p


# ---------------------------------------------------------------------------
# One-shot / dry-run reporting
# ---------------------------------------------------------------------------
class Style:
    """ANSI styling that no-ops when output is not a TTY (or NO_COLOR is set)."""

    _CODES = {
        "reset": "0", "bold": "1", "dim": "2",
        "red": "31", "green": "32", "yellow": "33", "blue": "34",
        "magenta": "35", "cyan": "36", "grey": "90",
    }

    def __init__(self, enabled: bool) -> None:
        self.enabled = enabled

    def paint(self, text: str, *styles: str) -> str:
        if not self.enabled or not styles:
            return text
        codes = ";".join(self._CODES[s] for s in styles if s in self._CODES)
        return f"\033[{codes}m{text}\033[0m" if codes else text


def _term_width() -> int:
    return max(60, shutil.get_terminal_size((100, 24)).columns)


def _collapse_home(path: Path) -> str:
    s = str(path)
    home = str(Path.home())
    return "~" + s[len(home):] if s.startswith(home) else s


def _shorten_middle(text: str, maxlen: int) -> str:
    """Ellipsize the middle so both the start and the (often distinctive) end survive."""
    if maxlen < 8 or len(text) <= maxlen:
        return text
    keep = maxlen - 1
    head = (keep + 1) // 2
    tail = keep - head
    return text[:head] + "…" + text[-tail:] if tail else text[:head] + "…"


def _detail(style: Style, icon: str, label: str, value: str, width: int,
            value_style: tuple[str, ...] = ()) -> str:
    """One indented 'icon  label  value' line, truncating a long value to width."""
    indent = "     "
    label_col = 8
    raw_prefix = f"{indent}{icon}  {label:<{label_col}}"
    avail = max(12, width - len(raw_prefix) - 1)
    value = _shorten_middle(value, avail)
    prefix = f"{indent}{style.paint(icon, 'grey')}  {style.paint(f'{label:<{label_col}}', 'grey')}"
    return prefix + (style.paint(value, *value_style) if value_style else value)


def _render_plan(fp: FilePlan, cfg: Config, style: Style, width: int) -> list[str]:
    """Render a single file's plan as a list of printable lines."""
    lines: list[str] = []
    name = fp.path.name

    # -- headline: status icon + filename -----------------------------
    if fp.status == "error":
        icon, name_style, tail = "✗", ("red", "bold"), style.paint(f"  {fp.reason}", "red")
    elif fp.status == "skip":
        icon, name_style, tail = "⏭", ("yellow",), style.paint(f"  {fp.reason}", "dim")
    else:
        icon, name_style, tail = "✓", ("green", "bold"), ""
    headline_prefix = f"  {style.paint(icon, *(('red',) if fp.status=='error' else ('yellow',) if fp.status=='skip' else ('green',)))} "
    shown_name = _shorten_middle(name, width - 6 - len(fp.reason or ""))
    lines.append(f"{headline_prefix}{style.paint(shown_name, *name_style)}{tail}")

    # -- what was read ------------------------------------------------
    if fp.date_value:
        read_val = f"{fp.date_tag.split(':')[-1]} = {fp.date_value}"
    else:
        read_val = "no embedded date"
    cam = " · ".join(p for p in (fp.make.strip(), fp.model.strip()) if p) or "no camera info"
    lines.append(_detail(style, "🔎", "read", f"{read_val}   ·   {cam}", width, ("cyan",)))

    if fp.status == "error":
        return lines
    if fp.status == "skip":
        # Show provenance we would have preserved, then stop.
        if fp.original_value:
            lines.append(_detail(style, "🏷", "tracked", f"{cfg.original_name_tag} = {fp.original_value}", width, ("dim",)))
        return lines

    # -- resolved date ------------------------------------------------
    if fp.resolved_date:
        origin_note = style.paint(f"({fp.date_origin} · {fp.date_precision})", "dim")
        shown = fp.resolved_date.replace(microsecond=0).isoformat(sep=" ")
        lines.append(_detail(style, "📅", "date", f"{shown}  {origin_note}", width))

    # -- metadata that would be written -------------------------------
    if fp.writes:
        parts = []
        for tag, val in fp.writes.items():
            parts.append(f"{tag.split(':')[-1]}={val}")
        write_val = "  ·  ".join(parts)
        lines.append(_detail(style, "🏷", "write", write_val, width, ("magenta",)))
    if fp.original_present:
        lines.append(_detail(style, "🔒", "keep", f"{cfg.original_name_tag} already set: {fp.original_value}", width, ("dim",)))

    # -- destination --------------------------------------------------
    if fp.target:
        try:
            rel = fp.target.relative_to(cfg.target_directory)
            dest = str(rel)
        except ValueError:
            dest = _collapse_home(fp.target)
        note = style.paint("  (name taken → suffixed)", "yellow") if fp.collision else ""
        lines.append(_detail(style, "➜", "rename", dest, width, ("green",)) + note)

    return lines


def _iter_source_media(cfg: Config, source: Path) -> Iterable[Path]:
    """Walk *source* for media, skipping hidden dirs and the error subtree."""
    for root, dirs, files in os.walk(source):
        # Prune hidden directories and the error dir (when distinct from source).
        dirs[:] = [d for d in sorted(dirs) if not d.startswith(".")]
        root_path = Path(root)
        if cfg.error_directory != source and _is_within(root_path, cfg.error_directory):
            dirs[:] = []
            continue
        for name in sorted(files):
            p = root_path / name
            if p.suffix.lower() in cfg.extensions:
                yield p


def run_oneshot(source: Path, target: Optional[Path], dry_run: bool,
                color: Optional[bool], verbose: bool) -> int:
    """Process a directory in a single pass, printing a human-readable report."""
    source = Path(source).expanduser().resolve()
    if not source.is_dir():
        print(f"ERROR: source is not a directory: {source}", file=sys.stderr)
        return 2

    enabled = (sys.stdout.isatty() and os.environ.get("NO_COLOR") is None) if color is None else color
    style = Style(enabled)
    width = _term_width()

    cfg = Config.defaults(source, target)

    # Reading metadata never needs the custom-namespace config; writing does.
    exiftool_config = None
    if not dry_run:
        exiftool_config = ensure_exiftool_config(cfg, cfg.target_directory / "_media_organizer")

    # Quiet logging: this mode talks to the user via the report, not the log.
    logging.basicConfig(level=logging.DEBUG if verbose else logging.ERROR,
                        format="%(levelname)s %(message)s")

    meta = MetadataService(cfg, exiftool_config)
    meta.start()
    processor = MediaProcessor(cfg, meta)

    mode = style.paint("DRY RUN", "yellow", "bold") if dry_run else style.paint("EXECUTE", "red", "bold")
    print()
    print(f"  {style.paint('media_organizer', 'bold')}  [{mode}]")
    print(f"  {style.paint('source', 'grey')}  {_shorten_middle(_collapse_home(source), width - 12)}")
    print(f"  {style.paint('pattern', 'grey')} {_shorten_middle(cfg.renaming_pattern, width - 12)}")

    counts = {"plan": 0, "skip": 0, "error": 0}
    last_dir: Optional[Path] = None

    try:
        for path in _iter_source_media(cfg, source):
            fp = processor.plan(path)
            counts[fp.status] = counts.get(fp.status, 0) + 1

            # Group under a directory header so long paths print only once.
            if path.parent != last_dir:
                last_dir = path.parent
                header = _collapse_home(path.parent)
                print()
                print(f"  {style.paint('📁 ' + _shorten_middle(header, width - 6), 'blue', 'bold')}")

            for line in _render_plan(fp, cfg, style, width):
                print(line)

            # In execute mode, actually apply after showing the plan.
            if not dry_run and fp.status == "plan":
                processor.process(path)
    finally:
        meta.close()

    # -- summary ------------------------------------------------------
    print()
    print("  " + style.paint("─" * (width - 4), "grey"))
    total = sum(counts.values())
    summary = (
        f"  {style.paint('✓ ' + str(counts['plan']), 'green')} to process   "
        f"{style.paint('⏭ ' + str(counts['skip']), 'yellow')} skipped   "
        f"{style.paint('✗ ' + str(counts['error']), 'red')} errors   "
        f"{style.paint(str(total) + ' scanned', 'grey')}"
    )
    print(summary)
    if dry_run and counts["plan"]:
        print("  " + style.paint("Re-run with --execute to apply these changes.", "dim"))
    print()
    return 0


# ---------------------------------------------------------------------------
# Logging + entry point
# ---------------------------------------------------------------------------
def _setup_logging(log_file: Path, verbose: bool) -> None:
    log_file.parent.mkdir(parents=True, exist_ok=True)
    level = logging.DEBUG if verbose else logging.INFO
    LOG.setLevel(level)
    LOG.handlers.clear()

    fmt = logging.Formatter(
        "%(asctime)s %(levelname)-7s [%(threadName)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )

    file_handler = logging.handlers.RotatingFileHandler(
        log_file, maxBytes=10 * 1024 * 1024, backupCount=5, encoding="utf-8"
    )
    file_handler.setFormatter(fmt)
    LOG.addHandler(file_handler)

    console = logging.StreamHandler(sys.stdout)
    console.setFormatter(fmt)
    LOG.addHandler(console)


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="Media asset watch/organize daemon (with a config-less one-shot mode)")
    parser.add_argument("-c", "--config", type=Path, help="Path to YAML config (daemon/watch mode)")
    parser.add_argument("-s", "--source", type=Path,
                        help="One-shot mode: process this directory once, bypassing the config")
    parser.add_argument("-t", "--target", type=Path,
                        help="One-shot mode: destination root (default: organize in place under source)")
    parser.add_argument("--dry-run", action="store_true",
                        help="One-shot mode: report only; never write metadata or move files")
    parser.add_argument("--execute", action="store_true",
                        help="One-shot mode: actually apply the changes (default is dry-run)")
    parser.add_argument("--no-color", action="store_true", help="Disable coloured output")
    parser.add_argument("-v", "--verbose", action="store_true", help="Debug logging")
    parser.add_argument(
        "--no-scan",
        action="store_true",
        help="Daemon mode: do not process files already present at startup",
    )
    args = parser.parse_args(argv)

    # -- routing / validation --------------------------------------------
    if bool(args.config) == bool(args.source):
        parser.error("provide exactly one of -c/--config (watch mode) or -s/--source (one-shot mode)")
    if args.dry_run and args.execute:
        parser.error("--dry-run and --execute are mutually exclusive")

    # -- one-shot mode ----------------------------------------------------
    if args.source:
        color = False if args.no_color else None
        # Safe by default: only touch files when --execute is explicitly given.
        dry_run = not args.execute
        return run_oneshot(args.source, args.target, dry_run, color, args.verbose)

    # -- daemon / watch mode ----------------------------------------------
    try:
        cfg = Config.load(args.config)
    except (OSError, ValueError, yaml.YAMLError) as err:
        print(f"FATAL: could not load config: {err}", file=sys.stderr)
        return 2

    _setup_logging(cfg.log_file, args.verbose)
    LOG.info("Starting media_organizer")
    LOG.info("Watching: %s", cfg.watch_directory)
    LOG.info("Target:   %s", cfg.target_directory)
    LOG.info("Errors:   %s", cfg.error_directory)

    # Store the auto-generated ExifTool config alongside the log file.
    exiftool_config = ensure_exiftool_config(cfg, cfg.log_file.parent)

    meta = MetadataService(cfg, exiftool_config)
    meta.start()
    processor = MediaProcessor(cfg, meta)

    work_queue: "queue.Queue[Path]" = queue.Queue()
    worker = Worker(cfg, work_queue, processor)
    worker.start()

    handler = _EnqueueHandler(work_queue, processor)
    observer = Observer()  # inotify-backed on Linux
    observer.schedule(handler, str(cfg.watch_directory), recursive=True)
    observer.start()

    # Optional initial sweep of pre-existing files.
    if cfg.process_existing_on_start and not args.no_scan:
        count = 0
        for p in _iter_existing_media(cfg):
            work_queue.put(p)
            count += 1
        LOG.info("Enqueued %d pre-existing file(s) for processing", count)

    # Graceful shutdown on SIGINT/SIGTERM.
    stop_event = threading.Event()

    def _handle_signal(signum, _frame):
        LOG.info("Received signal %s, shutting down...", signal.Signals(signum).name)
        stop_event.set()

    signal.signal(signal.SIGINT, _handle_signal)
    signal.signal(signal.SIGTERM, _handle_signal)

    try:
        while not stop_event.is_set():
            time.sleep(0.5)
    finally:
        LOG.info("Stopping observer...")
        observer.stop()
        observer.join(timeout=5)
        worker.stop()
        worker.join(timeout=10)
        meta.close()
        LOG.info("Shutdown complete")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
