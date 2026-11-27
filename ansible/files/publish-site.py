import base64
import fcntl
import io
import os
from pathlib import Path
import shutil
import sys
import tempfile
import zipfile


def publish(root, encoded):
    if len(encoded) > 700000:
        raise ValueError("Site archive too large")
    root.mkdir(mode=0o755, parents=True, exist_ok=True)
    with (root / ".lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        with zipfile.ZipFile(io.BytesIO(base64.b64decode(encoded, validate=True))) as archive:
            entries = archive.infolist()
            if len(entries) > 128 or sum(entry.file_size for entry in entries) > 3 << 20:
                raise ValueError("Expanded site exceeds limits")
            release = Path(tempfile.mkdtemp(prefix="release-", dir=root))
            link = root / (release.name + ".link")
            try:
                for entry in entries:
                    names = entry.filename.rstrip("/").split("/")
                    if any(not name or name.startswith(".") or "\\" in name for name in names):
                        raise ValueError("Unsafe file path")
                    if entry.is_dir():
                        continue
                    target = release.joinpath(*names)
                    target.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
                    with archive.open(entry) as source, target.open("xb") as dest:
                        shutil.copyfileobj(source, dest)
                    target.chmod(0o644)
                if not (release / "index.html").stat().st_size:
                    raise ValueError("Missing index.html")
                release.chmod(0o755)
                link.symlink_to(release.name)
                link.replace(root / "current")
            except Exception:
                link.unlink(missing_ok=True)
                shutil.rmtree(release)
                raise
        for prior in root.glob("release-*"):
            if prior != release and prior.is_dir() and not prior.is_symlink():
                shutil.rmtree(prior, ignore_errors=True)


if __name__ == "__main__":
    publish(Path("/var/www/kvn-site-archive"), Path(sys.argv[1]).read_bytes())
