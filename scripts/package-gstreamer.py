"""Build the portable Windows capture package from verified upstream runtimes."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
GST_SHA256 = "a863bf3faa49e9f33bd3cc42967b473482d4dc98655ed95cba1ac59f26fb0cfb"

def verify(path, expected):
    with path.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != expected:
            raise ValueError(f"Unexpected SHA256: {path}")

def package(args):
    deps = json.loads((ROOT / "setup/dependencies.json").read_text(encoding="utf-8-sig"))
    verify(args.gst, GST_SHA256)
    verify(args.ffmpeg, deps["ffmpeg"]["sha256"])
    verify(args.vcredist, deps["vcredist"]["sha256"])
    if args.output.exists():
        raise FileExistsError(args.output)
    with tempfile.TemporaryDirectory(dir=args.output.parent) as tmp:
        stage = Path(tmp).resolve()
        subprocess.run(["msiexec.exe", "/a", str(args.gst.resolve()), "/qn", f"TARGETDIR={stage}"], check=True)
        gst = next(stage.rglob("gst-launch-1.0.exe")).parent.parent
        archive = stage / "package.zip"
        with zipfile.ZipFile(archive, "x", zipfile.ZIP_DEFLATED, compresslevel=2) as out:
            out.write(args.exe, "LocalBroadcast.exe")
            out.write(args.vcredist, "VC_redist.x64.exe")
            out.write(ROOT / "docs/GSTREAMER_WINDOWS.md", "README.md")
            out.write(ROOT / "docs/GSTREAMER_ACCEPTANCE.md", "ACCEPTANCE.md")
            out.write(ROOT / "tools/obs-latency/Test-OBSDecoder.ps1","Test-OBSDecoder.ps1")
            for path in gst.rglob("*"):
                if path.is_file():
                    out.write(path, "gstreamer/" + path.relative_to(gst).as_posix())
            with zipfile.ZipFile(args.ffmpeg) as ff:
                for item in ff.infolist():
                    name = Path(item.filename).name
                    if name in ("ffmpeg.exe","ffplay.exe","ffprobe.exe","LICENSE","README.txt"):
                        with ff.open(item) as src, out.open(name if name.endswith(".exe") else "ffmpeg-notices/"+name,"w") as dst:
                            import shutil
                            shutil.copyfileobj(src, dst)
        with zipfile.ZipFile(archive) as z:
            required = {"LocalBroadcast.exe","ffmpeg.exe","ffplay.exe","gstreamer/bin/gst-launch-1.0.exe","gstreamer/bin/gst-device-monitor-1.0.exe"}
            if not required.issubset(z.namelist()) or z.testzip() is not None:
                raise ValueError("Package verification failed")
        archive.rename(args.output)
    with args.output.open("rb") as f:
        digest = hashlib.file_digest(f,"sha256").hexdigest()
    Path(str(args.output)+".sha256").write_text(f"{digest}  {args.output.name}\n", encoding="ascii")

if __name__ == "__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    for key in ("gst","ffmpeg","vcredist","exe","output"):
        parser.add_argument("--"+key,type=Path,required=True)
    package(parser.parse_args())
