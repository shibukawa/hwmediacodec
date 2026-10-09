"""Refresh the basis input_hash values (SHA-256 of each file) in the .knowledge
concept YAML files without reformatting them, after the tests a verification
entry names were re-run. Known renamed paths are rewritten first.

Usage: python3 scripts/knowledge_refresh_basis.py . [--apply]
(without --apply it only reports what would change)."""
import hashlib, os, re, sys

RENAMES = {
    "decode_darwin_test.go": "decode_test.go",
    "encode_darwin_test.go": "encode_test.go",
    "internal/h264/h264.go": "internal/h264/poc.go",
    "internal/h264/h264_test.go": "internal/h264/poc_test.go",
    "internal/nvidia/sys/cuda_linux.go": "internal/nvidia/sys/cuda.go",
    "internal/nvidia/sys/cuvid_linux.go": "internal/nvidia/sys/cuvid.go",
    "internal/nvidia/sys/nvenc_linux.go": "internal/nvidia/sys/nvenc.go",
    "internal/nvidia/sys/sys_linux_test.go": "internal/nvidia/sys/sys_test.go",
    "examples/screencast/recorder.go": "capture/recorder.go",
    "examples/screencast/recorder_test.go": "capture/recorder_test.go",
    "examples/screencast/sinks.go": "capture/sinks.go",
    "examples/container/demux.go": "mediacontainer/mp4/demux.go",
    "examples/container/mux.go": "mediacontainer/mp4/mux.go",
    "examples/container/sample.go": "mediacontainer/mp4/sample.go",
    "examples/container/stream.go": "mediacontainer/mp4/stream.go",
    "examples/container/packetsource.go": "mediacontainer/mp4/packetsource.go",
    "examples/container/segmenter.go": "mediacontainer/mp4/segmenter.go",
    "examples/container/videofile.go": "mediacontainer/mp4/videofile.go",
    "examples/container/container_test.go": "mediacontainer/mp4/mp4_test.go",
    "examples/container/util_test.go": "mediacontainer/mp4/util_test.go",
    "examples/container/stream_test.go": "mediacontainer/mp4/stream_test.go",
    "examples/container/packetsource_test.go": "mediacontainer/mp4/packetsource_test.go",
    "examples/container/segmenter_test.go": "mediacontainer/mp4/segmenter_test.go",
    "examples/container/videofile_test.go": "mediacontainer/mp4/videofile_test.go",
    "examples/container/assets_test.go": "examples/assets/assets_test.go",
}
root = sys.argv[1]
apply = "--apply" in sys.argv
path_re = re.compile(r"^(\s*(?:- )?path: )(\S+)\s*$")
hash_re = re.compile(r"^(\s*input_hash: )([0-9a-f]{64})\s*$")

def sha(p):
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()

total_renamed = total_updated = 0
missing = []
cdir = os.path.join(root, ".knowledge", "concepts")
for name in sorted(os.listdir(cdir)):
    if not name.endswith(".yaml"):
        continue
    fp = os.path.join(cdir, name)
    lines = open(fp, encoding="utf-8").read().split("\n")
    out = []
    last_path = None
    renamed = updated = 0
    for line in lines:
        m = path_re.match(line)
        if m:
            p = m.group(2)
            if p in RENAMES:
                p = RENAMES[p]
                line = m.group(1) + p
                renamed += 1
            last_path = p
            out.append(line)
            continue
        m = hash_re.match(line)
        if m and last_path:
            full = os.path.join(root, last_path)
            if os.path.exists(full):
                h = sha(full)
                if h != m.group(2):
                    line = m.group(1) + h
                    updated += 1
            else:
                missing.append((name, last_path))
        out.append(line)
    if (renamed or updated) and apply:
        open(fp, "w", encoding="utf-8").write("\n".join(out))
    if renamed or updated:
        print(f"{name}: {renamed} renamed, {updated} hashes updated")
    total_renamed += renamed
    total_updated += updated
print(f"total: {total_renamed} renamed, {total_updated} updated, applied={apply}")
for m in missing:
    print("missing:", m)
