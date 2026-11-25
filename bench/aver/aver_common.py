"""Helpers shared by the prep scripts. Standard library only."""
import hashlib
import json
import os
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))


def load_json(name):
    with open(os.path.join(HERE, name), encoding="utf-8") as f:
        return json.load(f)


def sha256_of(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def fetch(url, sha256, dest):
    """Download url to dest unless dest already has the pinned hash. A mismatch is fatal."""
    if os.path.exists(dest) and sha256_of(dest) == sha256:
        return dest
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    tmp = dest + ".part"
    req = urllib.request.Request(url, headers={"User-Agent": "hydra-bench-aver"})
    try:
        with urllib.request.urlopen(req, timeout=300) as r, open(tmp, "wb") as f:
            for block in iter(lambda: r.read(1 << 20), b""):
                f.write(block)
    except BaseException:
        # A cut download must not linger as a .part the next run mistakes for progress.
        if os.path.exists(tmp):
            os.remove(tmp)
        raise
    got = sha256_of(tmp)
    if got != sha256:
        os.remove(tmp)
        sys.exit(f"refusing {url}: sha256 is {got}, pinned {sha256}")
    os.replace(tmp, dest)
    return dest


def estimate_tokens(text):
    return (len(text) + 3) // 4


def write_jsonl(path, rows):
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        for row in rows:
            f.write(json.dumps(row, sort_keys=True, ensure_ascii=False) + "\n")


def read_jsonl(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]
