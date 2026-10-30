"""frigate-review: a small footage browser that reads Frigate's recording
segments directly off disk.

Frigate stores continuous recordings as <REC>/<YYYY-MM-DD>/<HH>/<camera>/<MM>.<SS>.mp4
(UTC). This server indexes those segments by time, renders timeline
thumbnails, and serves any time range per camera either remuxed as-is
("direct", original codec) or transcoded by ffmpeg to H.264 at a chosen
height so every browser can play it.

NOTE: this file is copied through stay-go's compose renderer, which expands
dollar-brace and dollar-paren tokens and tildes. Keep those sequences out.
"""

import hashlib
import json
import os
import re
import shutil
import subprocess
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

REC = os.environ.get("REC_DIR", "/recordings")
CACHE = os.environ.get("CACHE_DIR", "/cache")
STATIC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "static")
FRIGATE = os.environ.get("FRIGATE_URL", "http://host.docker.internal:5000").rstrip("/")
TZ_NAME = os.environ.get("TZ", "UTC")
HWACCEL = os.environ.get("HWACCEL", "vaapi")  # "vaapi" or "none"
VAAPI_DEVICE = os.environ.get("VAAPI_DEVICE", "/dev/dri/renderD128")
CACHE_MAX_GB = float(os.environ.get("CACHE_MAX_GB", "40"))
PORT = int(os.environ.get("PORT", "5080"))
MAX_CLIP = 3 * 3600

SEG_RE = re.compile(r"^(\d\d)\.(\d\d)\.mp4$")

thumb_sem = threading.BoundedSemaphore(int(os.environ.get("THUMB_WORKERS", "8")))
clip_sem = threading.BoundedSemaphore(int(os.environ.get("CLIP_WORKERS", "3")))

for sub in ("thumbs", "clips", "lists"):
    os.makedirs(os.path.join(CACHE, sub), exist_ok=True)


# ---------------------------------------------------------------- segments

_hour_cache = {}
_hour_lock = threading.Lock()


def hour_path(hour_epoch):
    d = datetime.fromtimestamp(hour_epoch, tz=timezone.utc)
    return os.path.join(REC, d.strftime("%Y-%m-%d"), d.strftime("%H"))


def hour_segments(cam, hour_epoch):
    """Sorted segment start times (epoch seconds) for one camera-hour."""
    key = (cam, hour_epoch)
    # Hours more than 2h old are immutable; cache them.
    immutable = hour_epoch < time.time() - 2 * 3600
    if immutable:
        with _hour_lock:
            if key in _hour_cache:
                return _hour_cache[key]
    path = os.path.join(hour_path(hour_epoch), cam)
    starts = []
    try:
        for name in os.listdir(path):
            m = SEG_RE.match(name)
            if m:
                starts.append(hour_epoch + int(m.group(1)) * 60 + int(m.group(2)))
    except FileNotFoundError:
        pass
    starts.sort()
    if immutable:
        with _hour_lock:
            _hour_cache[key] = starts
            if len(_hour_cache) > 20000:
                _hour_cache.clear()
    return starts


def seg_file(cam, start):
    hour = start - start % 3600
    d = datetime.fromtimestamp(start, tz=timezone.utc)
    return os.path.join(hour_path(hour), cam, d.strftime("%M.%S") + ".mp4")


def segments(cam, t0, t1):
    """[(start, dur, path)] for segments overlapping [t0, t1)."""
    h0 = int(t0 // 3600) * 3600 - 3600
    h1 = int(t1 // 3600) * 3600 + 3600
    starts = []
    for h in range(h0, h1 + 1, 3600):
        starts.extend(hour_segments(cam, h))
    out = []
    for i, s in enumerate(starts):
        if i + 1 < len(starts):
            dur = min(starts[i + 1] - s, 60)
        else:
            # Last known segment: assume the typical length seen so far.
            dur = min(10, max(1, (starts[i] - starts[i - 1]) if i else 10))
        if s + dur > t0 and s < t1:
            out.append((s, dur, seg_file(cam, s)))
    return out


def coverage(cam, t0, t1):
    out = []
    for s, d, _ in segments(cam, t0, t1):
        if out and s - out[-1][1] <= 2:
            out[-1][1] = s + d
        else:
            out.append([s, s + d])
    return out


def list_cameras():
    cams = set()
    try:
        cfg = frigate_json("/api/config")
        cams.update(cfg.get("cameras", {}).keys())
    except Exception:
        pass
    now = int(time.time())
    for back in range(0, 72):
        p = hour_path((now // 3600 - back) * 3600)
        try:
            cams.update(n for n in os.listdir(p) if os.path.isdir(os.path.join(p, n)))
        except FileNotFoundError:
            continue
    return sorted(cams)


def frigate_json(path, timeout=5):
    with urllib.request.urlopen(FRIGATE + path, timeout=timeout) as r:
        return json.load(r)


# ---------------------------------------------------------------- ffmpeg

def probe(path):
    try:
        out = subprocess.run(
            ["ffprobe", "-v", "error", "-show_entries",
             "stream=codec_type,codec_name,width,height", "-of", "json", path],
            capture_output=True, timeout=15, check=True).stdout
        return json.loads(out).get("streams", [])
    except Exception:
        return []


def real_audio(path):
    """True when the segment carries actual audio. Cameras without a mic still
    get a placeholder AAC track with a single packet per segment; keeping it
    leaves browsers waiting forever for audio samples on progressive playback."""
    try:
        out = subprocess.run(
            ["ffprobe", "-v", "error", "-select_streams", "a:0", "-count_packets",
             "-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path],
            capture_output=True, text=True, timeout=15).stdout.strip()
        return int(out or 0) > 5
    except (ValueError, subprocess.TimeoutExpired):
        return False


def make_thumb(cam, t, width):
    t = int(t)
    out = os.path.join(CACHE, "thumbs", cam, str(width), "%d.jpg" % t)
    if os.path.exists(out):
        return out
    segs = segments(cam, t - 30, t + 30)
    if not segs:
        return None
    seg = min(segs, key=lambda s: 0 if s[0] <= t < s[0] + s[1] else min(abs(s[0] - t), abs(s[0] + s[1] - t)))
    off = max(0.0, min(t - seg[0], seg[1] - 0.5))
    os.makedirs(os.path.dirname(out), exist_ok=True)
    tmp = out + ".%d.tmp.jpg" % threading.get_ident()
    with thumb_sem:
        if os.path.exists(out):
            return out
        r = subprocess.run(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-threads", "4",
             "-ss", "%.3f" % off, "-i", seg[2], "-frames:v", "1",
             "-vf", "scale=%d:-2:flags=fast_bilinear" % width, "-q:v", "5", "-y", tmp],
            capture_output=True, timeout=60)
    if r.returncode != 0 or not os.path.exists(tmp):
        try:
            os.remove(tmp)
        except OSError:
            pass
        return None
    os.replace(tmp, out)
    return out


class Job:
    def __init__(self, key):
        self.key = key
        self.part = os.path.join(CACHE, "clips", key + ".part.mp4")
        self.final = os.path.join(CACHE, "clips", key + ".mp4")
        self.done = threading.Event()
        self.ok = False
        self.error = None
        self.progress = 0.0
        self.encoder = None


jobs = {}
jobs_lock = threading.Lock()


def clip_key(cam, a, b, height, mode):
    raw = "%s|%d|%d|%s|%s" % (cam, a, b, height, mode)
    return hashlib.sha1(raw.encode()).hexdigest()[:20]


def write_list(key, segs, a, b):
    path = os.path.join(CACHE, "lists", key + ".txt")
    lines = []
    for i, (s, d, f) in enumerate(segs):
        lines.append("file '%s'" % f.replace("'", "'\\''"))
        if i == 0 and a > s:
            lines.append("inpoint %.3f" % (a - s))
        if i == len(segs) - 1 and b < s + d:
            lines.append("outpoint %.3f" % (b - s))
    with open(path, "w") as fh:
        fh.write("\n".join(lines) + "\n")
    return path


def ffmpeg_cmd(listfile, out, mode, height, has_audio, hw):
    cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-nostats",
           "-progress", "pipe:1", "-fflags", "+genpts"]
    if mode != "direct" and hw:
        cmd += ["-hwaccel", "vaapi", "-hwaccel_device", VAAPI_DEVICE,
                "-hwaccel_output_format", "vaapi"]
    cmd += ["-f", "concat", "-safe", "0", "-i", listfile, "-map", "0:v:0"]
    if has_audio:
        cmd += ["-map", "0:a:0?"]
    if mode == "direct":
        cmd += ["-c", "copy"]
        if has_audio:
            cmd += ["-c:a", "aac", "-b:a", "96k"]
    else:
        h = int(height) if str(height).isdigit() else 0
        if hw:
            vf = "scale_vaapi=w=-2:h=%d:format=nv12" % h if h else "scale_vaapi=format=nv12"
            cmd += ["-vf", vf, "-c:v", "h264_vaapi", "-qp", "26", "-g", "60"]
        else:
            vf = ("scale=-2:'min(%d,ih)':flags=bicubic," % h if h else "") + "format=yuv420p"
            cmd += ["-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
                    "-g", "60", "-threads", "0"]
        if has_audio:
            cmd += ["-c:a", "aac", "-b:a", "96k"]
    cmd += ["-movflags", "+frag_keyframe+empty_moov+default_base_moof",
            "-f", "mp4", "-y", out]
    return cmd


def run_ffmpeg(job, cmd, total):
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    for line in proc.stdout:
        if line.startswith("out_time_us=") and total > 0:
            try:
                job.progress = min(0.99, int(line.split("=")[1]) / 1e6 / total)
            except ValueError:
                pass
    err = proc.stderr.read()
    proc.wait()
    return proc.returncode, err


def run_job(job, cam, a, b, height, mode):
    try:
        segs = segments(cam, a, b)
        if not segs:
            raise RuntimeError("no recordings for %s in that range" % cam)
        listfile = write_list(job.key, segs, a, b)
        has_audio = real_audio(segs[min(1, len(segs) - 1)][2])
        total = b - a
        with clip_sem:
            attempts = [False] if mode == "direct" else ([True, False] if HWACCEL == "vaapi" else [False])
            rc, err = 1, ""
            for hw in attempts:
                job.encoder = "copy" if mode == "direct" else ("vaapi" if hw else "x264")
                rc, err = run_ffmpeg(job, ffmpeg_cmd(listfile, job.part, mode, height, has_audio, hw), total)
                if rc == 0:
                    break
                print("ffmpeg (%s) failed for %s: %s" % (job.encoder, job.key, err.strip()[-800:]), flush=True)
        if rc != 0:
            raise RuntimeError(err.strip()[-400:] or "ffmpeg failed")
        # Remux the fragmented stream into a regular, seekable faststart MP4.
        tmp = job.final + ".tmp.mp4"
        r = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-i", job.part,
                            "-map", "0", "-c", "copy", "-movflags", "+faststart", "-y", tmp],
                           capture_output=True, text=True)
        if r.returncode != 0:
            raise RuntimeError(r.stderr.strip()[-400:])
        os.replace(tmp, job.final)
        job.ok = True
        job.progress = 1.0
    except Exception as e:  # noqa: BLE001 — surfaced to the UI
        job.error = str(e)
        print("job %s failed: %s" % (job.key, e), flush=True)
    finally:
        job.done.set()
        prune_cache()


def start_clip(cam, a, b, height, mode):
    key = clip_key(cam, a, b, height, mode)
    with jobs_lock:
        job = jobs.get(key)
        if job and (job.ok or not job.done.is_set()):
            return job
        job = Job(key)
        if os.path.exists(job.final):
            job.ok = True
            job.progress = 1.0
            job.done.set()
            os.utime(job.final)
        else:
            for p in (job.part, job.final):
                try:
                    os.remove(p)
                except OSError:
                    pass
            threading.Thread(target=run_job, args=(job, cam, a, b, height, mode), daemon=True).start()
        jobs[key] = job
        return job


_prune_lock = threading.Lock()


def prune_cache():
    if not _prune_lock.acquire(blocking=False):
        return
    try:
        files = []
        for root, _, names in os.walk(CACHE):
            for n in names:
                p = os.path.join(root, n)
                try:
                    st = os.stat(p)
                except OSError:
                    continue
                files.append((st.st_mtime, st.st_size, p))
        total = sum(f[1] for f in files)
        limit = CACHE_MAX_GB * 1024 ** 3
        if total <= limit:
            return
        active = set()
        with jobs_lock:
            for j in jobs.values():
                if not j.done.is_set():
                    active.update((j.part, j.final))
        for mtime, size, p in sorted(files):
            if total <= limit * 0.8:
                break
            if p in active or time.time() - mtime < 600:
                continue
            try:
                os.remove(p)
                total -= size
            except OSError:
                pass
    finally:
        _prune_lock.release()


# ---------------------------------------------------------------- http

def fmt_name(cam, a, b, height, mode):
    d = datetime.fromtimestamp(a).strftime("%Y%m%d-%H%M%S")
    return "%s_%s_%ds_%s.mp4" % (cam, d, b - a, "orig" if mode == "direct" else (str(height) + "p" if height else "full"))


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "frigate-review"

    def log_message(self, fmt, *args):
        if os.environ.get("ACCESS_LOG"):
            super().log_message(fmt, *args)

    # -- helpers
    def send_json(self, obj, code=200):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def send_err(self, code, msg):
        self.send_json({"error": msg}, code)

    def send_file(self, path, ctype, cache="no-cache", download=None):
        try:
            size = os.path.getsize(path)
        except OSError:
            return self.send_err(404, "not found")
        start, end = 0, size - 1
        rng = self.headers.get("Range")
        status = 200
        if rng:
            m = re.match(r"bytes=(\d*)-(\d*)", rng)
            if m:
                if m.group(1):
                    start = int(m.group(1))
                    if m.group(2):
                        end = min(int(m.group(2)), size - 1)
                elif m.group(2):
                    start = max(0, size - int(m.group(2)))
                if start > end:
                    self.send_response(416)
                    self.send_header("Content-Range", "bytes */%d" % size)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                status = 206
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(end - start + 1))
        self.send_header("Cache-Control", cache)
        if status == 206:
            self.send_header("Content-Range", "bytes %d-%d/%d" % (start, end, size))
        if download:
            self.send_header("Content-Disposition", 'attachment; filename="%s"' % download)
        self.end_headers()
        if self.command == "HEAD":
            return
        with open(path, "rb") as fh:
            fh.seek(start)
            left = end - start + 1
            while left > 0:
                chunk = fh.read(min(1 << 20, left))
                if not chunk:
                    break
                self.wfile.write(chunk)
                left -= len(chunk)

    def tail_part(self, job):
        """Stream a still-growing fragmented MP4 until the job finishes."""
        deadline = time.time() + 60
        while not os.path.exists(job.part):
            if job.done.is_set() or time.time() > deadline:
                return self.send_err(500, job.error or "transcode did not start")
            time.sleep(0.1)
        self.send_response(200)
        self.send_header("Content-Type", "video/mp4")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        with open(job.part, "rb") as fh:
            while True:
                chunk = fh.read(1 << 18)
                if chunk:
                    self.wfile.write(chunk)
                    continue
                if job.done.is_set():
                    rest = fh.read()
                    if rest:
                        self.wfile.write(rest)
                    return
                time.sleep(0.15)

    # -- routing
    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        q = {k: v[-1] for k, v in urllib.parse.parse_qs(u.query).items()}
        try:
            route = ROUTES.get(u.path)
            if route:
                return route(self, q)
            if u.path.startswith("/media/"):
                return self.media(u.path[len("/media/"):], q)
            if u.path.startswith("/api/event_thumb/"):
                return self.event_thumb(u.path.rsplit("/", 1)[-1])
            name = "index.html" if u.path in ("/", "") else u.path.lstrip("/")
            path = os.path.normpath(os.path.join(STATIC, name))
            if not path.startswith(STATIC) or not os.path.isfile(path):
                return self.send_err(404, "not found")
            ctype = {"html": "text/html; charset=utf-8", "js": "text/javascript", "css": "text/css",
                     "svg": "image/svg+xml"}.get(path.rsplit(".", 1)[-1], "application/octet-stream")
            return self.send_file(path, ctype)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except ValueError as e:
            self.send_err(400, str(e))

    def api_meta(self, q):
        self.send_json({"tz": TZ_NAME, "now": time.time(), "cameras": list_cameras(),
                        "hwaccel": HWACCEL})

    def api_coverage(self, q):
        a, b = float(q["start"]), float(q["end"])
        if b - a > 8 * 86400:
            raise ValueError("range too large")
        cams = q["cams"].split(",") if q.get("cams") else list_cameras()
        self.send_json({c: coverage(c, a, b) for c in cams})

    def api_events(self, q):
        a, b = int(float(q["start"])), int(float(q["end"]))
        try:
            evs = frigate_json("/api/events?after=%d&before=%d&limit=2000&include_thumbnails=0" % (a, b), timeout=10)
        except Exception as e:  # noqa: BLE001
            return self.send_json({"events": [], "error": str(e)})
        out = []
        for e in evs:
            out.append({"id": e["id"], "camera": e["camera"], "label": e["label"],
                        "sub_label": e.get("sub_label"),
                        "start": e["start_time"], "end": e.get("end_time") or e["start_time"],
                        "score": (e.get("data") or {}).get("top_score") or e.get("top_score"),
                        "zones": e.get("zones") or []})
        out.sort(key=lambda e: e["start"])
        self.send_json({"events": out})

    def event_thumb(self, eid):
        if not re.match(r"^[\w.\-]+$", eid):
            return self.send_err(400, "bad id")
        try:
            with urllib.request.urlopen(FRIGATE + "/api/events/%s/thumbnail.jpg" % eid, timeout=10) as r:
                body = r.read()
        except Exception:  # noqa: BLE001
            return self.send_err(404, "no thumbnail")
        self.send_response(200)
        self.send_header("Content-Type", "image/jpeg")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "max-age=86400")
        self.end_headers()
        self.wfile.write(body)

    def api_thumb(self, q):
        cam = q["cam"]
        if cam not in list_cameras_cached():
            raise ValueError("unknown camera")
        width = max(80, min(1280, int(q.get("w", 320))))
        p = make_thumb(cam, float(q["t"]), width)
        if not p:
            self.send_response(204)
            self.send_header("Content-Length", "0")
            self.send_header("Cache-Control", "max-age=60")
            self.end_headers()
            return
        self.send_file(p, "image/jpeg", cache="max-age=31536000, immutable")

    def api_clip(self, q):
        cam = q["cam"]
        if cam not in list_cameras_cached():
            raise ValueError("unknown camera")
        a, b = int(float(q["start"])), int(float(q["end"]) + 0.999)
        if b <= a or b - a > MAX_CLIP:
            raise ValueError("bad range (max %ds)" % MAX_CLIP)
        mode = "direct" if q.get("mode") == "direct" else "transcode"
        height = q.get("h", "720")
        if not height.isdigit():
            height = "0"
        segs = segments(cam, a, b)
        if not segs:
            return self.send_json({"cam": cam, "available": False})
        real_a = max(a, segs[0][0])
        real_b = min(b, segs[-1][0] + segs[-1][1])
        job = start_clip(cam, a, b, height, mode)
        first = probe(segs[0][2])
        video = next((s for s in first if s.get("codec_type") == "video"), {})
        self.send_json({
            "cam": cam, "available": True, "key": job.key,
            "start": real_a, "end": real_b, "offset": real_a - a,
            "source": {"codec": video.get("codec_name"), "width": video.get("width"),
                       "height": video.get("height")},
            "stream": "/media/stream/" + job.key,
            "file": "/media/file/" + job.key,
            "download": "/media/file/%s?dl=%s" % (job.key, urllib.parse.quote(fmt_name(cam, a, b, int(height), mode))),
            "ready": job.ok, "error": job.error,
        })

    def api_job(self, q):
        job = jobs.get(q["key"])
        if not job:
            return self.send_err(404, "unknown job")
        self.send_json({"ready": job.ok, "done": job.done.is_set(), "error": job.error,
                        "progress": round(job.progress, 3), "encoder": job.encoder})

    def media(self, rest, q):
        kind, _, key = rest.partition("/")
        if not re.match(r"^[0-9a-f]{20}$", key):
            return self.send_err(400, "bad key")
        job = jobs.get(key)
        final = os.path.join(CACHE, "clips", key + ".mp4")
        if os.path.exists(final):
            dl = q.get("dl")
            if dl:
                dl = re.sub(r"[^\w.\-]", "_", dl)
            return self.send_file(final, "video/mp4", download=dl)
        if kind == "stream" and job and not job.error:
            return self.tail_part(job)
        if job and job.error:
            return self.send_err(500, job.error)
        return self.send_err(404, "not ready")


_cams = {"t": 0, "v": []}


def list_cameras_cached():
    if time.time() - _cams["t"] > 300:
        _cams["v"] = list_cameras()
        _cams["t"] = time.time()
    return _cams["v"]


ROUTES = {
    "/api/meta": Handler.api_meta,
    "/api/coverage": Handler.api_coverage,
    "/api/events": Handler.api_events,
    "/api/thumb": Handler.api_thumb,
    "/api/clip": Handler.api_clip,
    "/api/job": Handler.api_job,
}


def main():
    if HWACCEL == "vaapi" and not os.path.exists(VAAPI_DEVICE):
        print("VAAPI device %s missing; using software encoding" % VAAPI_DEVICE, flush=True)
        globals()["HWACCEL"] = "none"
    if shutil.which("ffmpeg") is None:
        raise SystemExit("ffmpeg not found")
    srv = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    srv.daemon_threads = True
    print("frigate-review on :%d (rec=%s cache=%s hw=%s)" % (PORT, REC, CACHE, HWACCEL), flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
