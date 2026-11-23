"""Writes a .tar.gz of one folder with Unix file modes, for the Linux and
macOS downloads (Windows has no executable bit to carry over).

    python scripts/tarball.py <folder> <out.tar.gz>

palate-agent and *.command files get 0755; everything else 0644.
"""
import os
import sys
import tarfile

src, out = sys.argv[1], sys.argv[2]
top = os.path.basename(os.path.normpath(src))


def mode(name):
    base = os.path.basename(name)
    return 0o755 if base == "palate-agent" or base.endswith(".command") else 0o644


with tarfile.open(out, "w:gz", format=tarfile.PAX_FORMAT) as tar:
    for root, dirs, files in os.walk(src):
        dirs.sort()
        rel = os.path.relpath(root, src)
        arc = top if rel == "." else top + "/" + rel.replace(os.sep, "/")
        info = tarfile.TarInfo(arc)
        info.type, info.mode, info.mtime = tarfile.DIRTYPE, 0o755, int(os.path.getmtime(root))
        tar.addfile(info)
        for f in sorted(files):
            path = os.path.join(root, f)
            info = tar.gettarinfo(path, arc + "/" + f)
            info.mode, info.uid, info.gid, info.uname, info.gname = mode(f), 0, 0, "root", "root"
            with open(path, "rb") as fh:
                tar.addfile(info, fh)
print(out)
