"""Refresh the basis input_hash values (SHA-256 of each file) in the .knowledge
concept YAML files without reformatting them, after the tests a verification
entry names were re-run.

Usage: python3 scripts/knowledge_refresh_basis.py . [--apply]
(without --apply it only reports what would change)."""
import hashlib, os, re, sys

root = sys.argv[1]
apply = "--apply" in sys.argv
path_re = re.compile(r"^(\s*(?:- )?path: )(\S+)\s*$")
hash_re = re.compile(r"^(\s*input_hash: )([0-9a-f]{64})\s*$")

def sha(p):
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()

total_updated = 0
missing = []
cdir = os.path.join(root, ".knowledge", "concepts")
for name in sorted(os.listdir(cdir)):
    if not name.endswith(".yaml"):
        continue
    fp = os.path.join(cdir, name)
    lines = open(fp, encoding="utf-8").read().split("\n")
    out = []
    last_path = None
    updated = 0
    for line in lines:
        m = path_re.match(line)
        if m:
            p = m.group(2)
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
    if updated and apply:
        open(fp, "w", encoding="utf-8").write("\n".join(out))
    if updated:
        print(f"{name}: {updated} hashes updated")
    total_updated += updated
print(f"total: {total_updated} updated, applied={apply}")
for m in missing:
    print("missing:", m)
