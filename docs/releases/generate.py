#!/usr/bin/env python3
"""Build the release notes pages under docs/releases/ from data, never by hand.

Sources, in the order a page uses them:
  CHANGELOG.md                 every feat/fix/perf line, its PR and its date
  data/vX.Y.Z.json             what GitHub knows: PR authors, linked issues, upgrade
                               notes written in PR bodies, release files, provenance,
                               published security advisories (written by `fetch`)
  notes/vX.Y.Z.md              optional hand-written summary, highlights, upgrade notes

Usage:
  python3 docs/releases/generate.py            write the pages, the feed and the sitemap block
  python3 docs/releases/generate.py --check    exit 1 if any of them is out of date
  python3 docs/releases/generate.py fetch [vX.Y.Z ...]
      refresh data/ from GitHub with `gh` (every version in CHANGELOG.md when none is named)
"""
import html
import json
import re
import subprocess
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parent.parent
REPO_ROOT = DOCS.parent
HERE = DOCS / "releases"
DATA = HERE / "data"
NOTES = HERE / "notes"
REPO = "ankit373/hydra"
GH = f"https://github.com/{REPO}"
SITE = "https://hydra.uvansa.com"

# release-please section titles, mapped to what the page calls them.
KINDS = {
    "Features": "new",
    "Bug Fixes": "fixed",
    "Performance Improvements": "faster",
    "Reverts": "changed",
    "⚠ BREAKING CHANGES": "changed",
}
KIND_TITLES = {"new": "New", "fixed": "Fixed", "faster": "Faster", "changed": "Changed or removed"}

# Commit scope to the area a user would look under. An unknown scope lands in Other.
AREAS = [
    ("Checking answers", "checkgen ground oracle trust editor evalset drift swarm verify compare stats"),
    ("Routing", "dispatch rank decide recipe workflow parallel ope registry"),
    ("Cost", "cost pricing"),
    ("Models and providers", "executor"),
    ("Long conversations", "idle payload"),
    ("Serving API", "serve"),
    ("Privacy and security", "policy security sandbox gateway"),
    ("Terminal app and CLI", "tui cli"),
    ("Desktop app", "desktop brand"),
    ("Install and upgrade", "install npm upgrade dist shellpath release"),
    ("Benchmarks", "bench"),
    ("Website and docs", "site blog design docs"),
    ("Project", "ci"),
]
AREA_OF = {s: name for name, scopes in AREAS for s in scopes.split()}
AREA_ORDER = [name for name, _ in AREAS] + ["Other"]

# The first release whose archives carry build provenance (#1410).
FIRST_ATTESTED = (1, 8, 0)

OS_NAMES = {"darwin": "macOS", "linux": "Linux", "windows": "Windows"}
ARCH_NAMES = {"amd64": "x86-64", "arm64": "ARM64", "universal": "universal"}


def vkey(v):
    return tuple(int(x) for x in v.split("."))


# ── CHANGELOG ────────────────────────────────────────────────────────────────

HEAD_RE = re.compile(r"^## \[(\d+\.\d+\.\d+)\]\(([^)]*)\) \((\d{4}-\d{2}-\d{2})\)")
LINE_RE = re.compile(r"^\* (?:\*\*([^*]+):\*\* )?(.*)$")
REF_RE = re.compile(r"\[#(\d+)\]\([^)]*\)")
HASH_RE = re.compile(r"\(\[[0-9a-f]{7}\]\([^)]*\)\)")


# Names that keep their lower case at the start of a line.
LOWER_WORDS = {"hyctl", "hydra", "npm", "pip", "brew", "gh", "npx"}


def capitalise(text):
    first = text.split(" ", 1)[0]
    if first.lower() in LOWER_WORDS or not first[:1].isalpha():
        return text
    return text[:1].upper() + text[1:]


def parse_item(scope, rest):
    """One changelog bullet: its words, the PR that landed it, and the issues it names."""
    hm = HASH_RE.search(rest)
    before, after = (rest[: hm.start()], rest[hm.end():]) if hm else (rest, "")
    refs = [int(n) for n in REF_RE.findall(before)]
    closes = [int(n) for n in REF_RE.findall(after)]
    text = re.sub(r"\s*\(\[#\d+\]\([^)]*\)\)", "", before).strip()
    scope = (scope or "").split(",")[0].strip()
    if scope == "release":
        # "v1.5.0, then a dash and what shipped": keep only what shipped.
        text = re.sub(r"^v\d+\.\d+\.\d+\s*(?:[\u2014\u2013-]\s*)?", "", text)
        text = text or "Release pull request; it lists what shipped"
    text = text.replace(" \u2014 ", ", ").replace("\u2014", ", ")  # no em dashes on the site
    return {
        "scope": scope,
        "area": AREA_OF.get(scope, "Other"),
        "text": capitalise(text),
        "pr": refs[-1] if refs else None,
        "issues": refs[:-1] + closes,
    }


def parse_changelog(text):
    """Every release-please section, newest first, with its items by kind."""
    releases, cur, kind = [], None, None
    for line in text.splitlines():
        m = HEAD_RE.match(line)
        if m:
            cur = {"version": m.group(1), "compare": m.group(2), "date": m.group(3), "items": []}
            releases.append(cur)
            kind = None
            continue
        if line.startswith("## "):
            cur = None  # the hand-kept pre-1.0 history is not a release page
            continue
        if cur is None:
            continue
        if line.startswith("### "):
            kind = KINDS.get(line[4:].strip())
            continue
        m = LINE_RE.match(line)
        if m and kind:
            item = parse_item(m.group(1), m.group(2))
            item["kind"] = kind
            cur["items"].append(item)
    return releases


def changelog():
    return parse_changelog((REPO_ROOT / "CHANGELOG.md").read_text())


# ── Hand-written notes (optional) ────────────────────────────────────────────


def parse_notes(text):
    """## Summary / ## Highlights / ## Upgrade notes / ## Security, each kept as markdown."""
    out, name, buf = {}, None, []
    for line in text.splitlines():
        if line.startswith("## "):
            if name:
                out[name] = "\n".join(buf).strip()
            name, buf = line[3:].strip().lower(), []
        elif name:
            buf.append(line)
    if name:
        out[name] = "\n".join(buf).strip()
    return out


def load_notes(version):
    p = NOTES / f"v{version}.md"
    return parse_notes(p.read_text()) if p.exists() else {}


# ── Markdown, the small subset notes and PR sections use ─────────────────────


def inline(s):
    parts = re.split(r"(`[^`]+`)", s)
    out = []
    for part in parts:
        if part.startswith("`") and part.endswith("`") and len(part) > 1:
            out.append(f'<code translate="no">{html.escape(part[1:-1])}</code>')
            continue
        e = html.escape(part, quote=False)
        e = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", e)
        e = re.sub(r"\[([^\]]+)\]\((https?://[^)\s]+)\)", r'<a href="\2">\1</a>', e)
        # A bare #n is a pull request or issue; GitHub redirects either way.
        e = re.sub(r"(?<![\w&/>\"])#(\d+)\b", rf'<a href="{GH}/pull/\1">#\1</a>', e)
        out.append(e)
    return "".join(out)


def markdown(md, h=3):
    """Paragraphs, '- ' lists and '### ' subheadings."""
    out, para, items = [], [], []

    def flush():
        if para:
            out.append(f"<p>{inline(' '.join(para))}</p>")
            para.clear()
        if items:
            out.append('<ul class="items">' + "".join(f"<li>{inline(i)}</li>" for i in items) + "</ul>")
            items.clear()

    for raw in md.splitlines():
        line = raw.rstrip()
        if not line.strip():
            flush()
        elif line.startswith("### "):
            flush()
            out.append(f'<div class="lbl">{inline(line[4:].strip())}</div>')
        elif re.match(r"^\s*[-*] ", line):
            if para:
                flush()
            items.append(re.sub(r"^\s*[-*] ", "", line))
        elif items and raw.startswith("  "):
            items[-1] += " " + line.strip()
        else:
            if items:
                flush()
            para.append(line.strip())
    flush()
    return "\n".join(out)


def plain(md):
    """Markdown to one line of text, for the feed and meta descriptions."""
    s = re.sub(r"`([^`]+)`", r"\1", md)
    s = re.sub(r"\*\*(.+?)\*\*", r"\1", s)
    s = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", s)
    return " ".join(s.split())


# ── GitHub data (fetch) ──────────────────────────────────────────────────────


def gh(*args):
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True, timeout=120).stdout


UPGRADE_RE = re.compile(r"(?ims)^#{2,3}\s*upgrade notes?\s*$(.*?)(?=^#{1,3}\s|\Z)")
BREAKING_RE = re.compile(r"(?ms)^BREAKING[ -]CHANGE:\s*(.+?)(?=\n\s*\n|\Z)")


def upgrade_notes(body):
    """What a PR body tells users to do: its '## Upgrade notes' section or BREAKING CHANGE footer."""
    body = re.sub(r"(?s)<!--.*?-->", "", body or "")
    found = [m.strip() for m in UPGRADE_RE.findall(body)]
    found += [" ".join(m.split()) for m in BREAKING_RE.findall(body)]
    nothing = re.compile(r"(?i)^(none|n/a|no|nothing)\b")  # "None. This only changes CI."
    return "\n\n".join(f for f in found if f and not nothing.match(f))


def fetch_prs(numbers):
    out = {}
    numbers = sorted(set(numbers))
    for i in range(0, len(numbers), 40):
        chunk = numbers[i: i + 40]
        fields = "number author{login __typename} closingIssuesReferences(first:20){nodes{number}} body"
        q = " ".join(f"p{n}:pullRequest(number:{n}){{{fields}}}" for n in chunk)
        owner, name = REPO.split("/")
        res = json.loads(gh("api", "graphql", "-f", f'query={{repository(owner:"{owner}",name:"{name}"){{{q}}}}}'))
        for pr in res["data"]["repository"].values():
            if not pr:
                continue
            author = pr["author"] or {}
            out[str(pr["number"])] = {
                "author": author.get("login") if author.get("__typename") == "User" else None,
                "issues": sorted(n["number"] for n in pr["closingIssuesReferences"]["nodes"]),
                "upgrade_notes": upgrade_notes(pr["body"]),
            }
    return out


def fetch_release(version):
    try:
        rel = json.loads(gh("api", f"repos/{REPO}/releases/tags/v{version}"))
    except subprocess.CalledProcessError as err:
        if "HTTP 404" not in (err.stderr or ""):
            raise
        return None  # not published yet: the page says so and links the tag
    assets = sorted(
        ({"name": a["name"], "size": a["size"], "digest": a.get("digest") or ""} for a in rel["assets"]),
        key=lambda a: a["name"],
    )
    attested = False
    if vkey(version) >= FIRST_ATTESTED:
        probe = next((a for a in assets if a["name"].startswith("hydra_") and a["digest"]), None)
        try:
            attested = bool(probe) and int(gh("api", f"repos/{REPO}/attestations/{probe['digest']}",
                                              "--jq", ".attestations|length")) > 0
        except subprocess.CalledProcessError as err:
            if "HTTP 404" not in (err.stderr or ""):
                raise  # a rate limit must not turn into "not attested" on the page
            attested = False
    return {"published": rel["published_at"][:10], "assets": assets, "attested": attested}


def fetch_advisories():
    """Published repository advisories only: a draft is never read into a page."""
    raw = json.loads(gh("api", f"repos/{REPO}/security-advisories?state=published&per_page=100"))
    out = []
    for a in raw:
        patched = " ".join(v.get("patched_versions") or "" for v in a.get("vulnerabilities") or [])
        out.append({
            "id": a["ghsa_id"], "cve": a.get("cve_id"), "summary": a["summary"],
            "severity": a.get("severity"), "url": a["html_url"], "patched": patched,
        })
    return out


def fetch(versions):
    releases = {r["version"]: r for r in changelog()}
    versions = [v.lstrip("v") for v in versions] or list(releases)
    advisories = fetch_advisories()
    DATA.mkdir(exist_ok=True)
    for v in versions:
        if v not in releases:
            raise SystemExit(f"v{v} is not in CHANGELOG.md")
        prs = [i["pr"] for i in releases[v]["items"] if i["pr"]]
        data = {
            "version": v,
            "prs": fetch_prs(prs),
            "release": fetch_release(v),
            "advisories": [a for a in advisories if re.search(rf"(?<![\d.]){re.escape(v)}(?![\d.])", a["patched"])],
        }
        (DATA / f"v{v}.json").write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
        print(f"wrote {DATA / f'v{v}.json'}")


def load_data(version):
    p = DATA / f"v{version}.json"
    if not p.exists():
        raise SystemExit(f"{p} is missing; run: python3 docs/releases/generate.py fetch v{version}")
    return json.loads(p.read_text())


# ── Rendering ────────────────────────────────────────────────────────────────

e = html.escape


def human_date(d):
    y, m, dd = d.split("-")
    months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split()
    return f"{months[int(m) - 1]} {int(dd)}, {y}"


def bump(v, prev):
    if prev is None:
        return "first"
    a, b = vkey(v), vkey(prev)
    return "major" if a[0] != b[0] else "minor" if a[1] != b[1] else "patch"


def enrich(rel):
    """Join each changelog line with what GitHub knows about its PR; read data and notes once."""
    data = rel["data"] = load_data(rel["version"])
    rel["notes"] = load_notes(rel["version"])
    for item in rel["items"]:
        pr = data["prs"].get(str(item["pr"]), {})
        item["author"] = pr.get("author")
        item["issues"] = sorted(set(item["issues"]) | set(pr.get("issues", [])))
        item["upgrade"] = pr.get("upgrade_notes", "")
    return rel


def counts(rel):
    c = {}
    for i in rel["items"]:
        c[i["kind"]] = c.get(i["kind"], 0) + 1
    return c


def count_line(rel):
    c = counts(rel)
    parts = [f"{c[k]} {KIND_TITLES[k].lower()}" for k in KIND_TITLES if c.get(k)]
    return ", ".join(parts) or "no user-facing changes listed"


def generated_summary(rel):
    """Said from the counts and areas alone, so nothing in it is made up."""
    areas = {}
    for i in rel["items"]:
        areas[i["area"]] = areas.get(i["area"], 0) + 1
    top = sorted(areas, key=lambda a: (-areas[a], AREA_ORDER.index(a)))[:3]
    if len(rel["items"]) == 1:
        return f"One change, in {top[0].lower()}: {rel['items'][0]['text'].rstrip('.')}."
    return f"{count_line(rel).capitalize()}. Most of the work is in {', '.join(a.lower() for a in top)}."


def summary(rel, notes):
    if notes.get("summary"):
        return plain(notes["summary"]), False
    return generated_summary(rel), True


def item_html(i):
    refs = []
    if i["pr"]:
        refs.append(f'<a href="{GH}/pull/{i["pr"]}">#{i["pr"]}</a>')
    issues = [n for n in i["issues"] if n != i["pr"]]
    if issues:
        refs.append("closes " + ", ".join(f'<a href="{GH}/issues/{n}">#{n}</a>' for n in issues))
    if i.get("author"):
        refs.append(f'<a href="https://github.com/{e(i["author"])}">@{e(i["author"])}</a>')
    scope = f'<span class="scope" translate="no">{e(i["scope"])}</span> ' if i["scope"] else ""
    return f'<li>{scope}{inline(i["text"])} <span class="refs">{" · ".join(refs)}</span></li>'


def kind_section(rel, kind, sid):
    items = [i for i in rel["items"] if i["kind"] == kind]
    if not items:
        return ""
    groups = []
    for area in AREA_ORDER:
        rows = [i for i in items if i["area"] == area]
        if rows:
            groups.append(f'<div class="sub"><div class="lbl">{e(area)}</div>\n<ul class="items">\n'
                          + "\n".join(item_html(i) for i in rows) + "\n</ul></div>")
    return (f'<section class="doc" id="{sid}">\n<h2>{KIND_TITLES[kind]} <span class="tagline">{len(items)}</span></h2>\n'
            + "\n".join(groups) + "\n</section>")


def upgrade_html(rel, notes):
    parts = []
    if notes.get("upgrade notes"):
        parts.append(markdown(notes["upgrade notes"]))
    from_prs = [i for i in rel["items"] if i.get("upgrade")]
    if from_prs:
        parts.append('<div class="lbl">From the pull requests</div>')
        for i in from_prs:
            parts.append(f'<div class="from-pr">{markdown(i["upgrade"])}'
                         f'<p class="refs">from <a href="{GH}/pull/{i["pr"]}">#{i["pr"]}</a></p></div>')
    if not parts:
        return '<p class="sum">No upgrade steps were written down for this release.</p>'
    return '<div class="sub md">' + "\n".join(parts) + "</div>"


def security_html(data, notes):
    parts = []
    advs = data.get("advisories") or []
    if advs:
        rows = "\n".join(
            f'<li><a href="{e(a["url"])}">{e(a["id"])}</a>{" (" + e(a["cve"]) + ")" if a.get("cve") else ""}: '
            f'{e(a["summary"])}{" <span class=refs>" + e(a["severity"]) + "</span>" if a.get("severity") else ""}</li>'
            for a in advs)
        parts.append(f'<ul class="items">\n{rows}\n</ul>')
    if notes.get("security"):
        parts.append(markdown(notes["security"]))
    if not parts:
        return '<p class="sum">No published security advisory names this release.</p>'
    return '<div class="sub md">' + "\n".join(parts) + "</div>"


def size(n):
    return f"{n / 1_048_576:.1f} MB" if n >= 1_048_576 else f"{max(1, round(n / 1024))} KB"


ASSET_RE = re.compile(r"^hydra(-desktop)?_v?[\d.]+_([a-z]+)_([a-z0-9]+)\.(tar\.gz|zip)$")


def downloads_html(rel, data):
    v = rel["version"]
    tag = f"{GH}/releases/tag/v{v}"
    r = data.get("release")
    if not r:
        return (f'<p class="sum">The files are attached when the release is published, on '
                f'<a href="{tag}">the v{v} release page</a>.</p>')
    plats = {}
    for a in r["assets"]:
        m = ASSET_RE.match(a["name"])
        if m:
            plats.setdefault((m.group(2), m.group(3)), {})["app" if m.group(1) else "cli"] = a
    base = f"{GH}/releases/download/v{v}/"

    def cell(a):
        if not a:
            return "<td>not built</td>"
        return f'<td><a href="{base}{e(a["name"])}" translate="no">{e(a["name"])}</a> <span class="refs">{size(a["size"])}</span></td>'

    order = sorted(plats, key=lambda k: (list(OS_NAMES).index(k[0]) if k[0] in OS_NAMES else 9, k[1]))
    rows = "\n".join(
        f'<tr><td>{OS_NAMES.get(o, o)} {ARCH_NAMES.get(a, a)}</td>{cell(plats[(o, a)].get("cli"))}{cell(plats[(o, a)].get("app"))}</tr>'
        for o, a in order)
    table = (f'<table class="ladder"><thead><tr><th>Platform</th><th>Command line (hyctl)</th><th>Desktop app</th></tr></thead>\n'
             f'<tbody>\n{rows}\n</tbody></table>')
    names = {a["name"] for a in r["assets"]}
    extra = []
    if "checksums.txt" in names:
        extra.append(f'<a href="{base}checksums.txt">checksums.txt</a>')
    if any(n.endswith(".sbom.json") for n in names):
        extra.append("an SBOM for each command-line archive")
    extra_line = f'<p class="sum">Also attached: {", ".join(extra)}. Every file is on <a href="{tag}">the release page</a>.</p>'
    return table + "\n" + extra_line + "\n" + verify_html(v, r, order, plats)


def copyline(cmd):
    return (f'<div class="term"><div class="copyline"><code translate="no"><span class="p">$</span> {e(cmd)}</code>'
            f'<button type="button" class="copy-btn" data-copy="{e(cmd)}">copy</button></div></div>')


def verify_html(v, r, order, plats):
    sample = next((plats[k]["cli"]["name"] for k in order if k == ("linux", "amd64") and "cli" in plats[k]), None)
    sample = sample or next((plats[k]["cli"]["name"] for k in order if "cli" in plats[k]), "")
    out = ['<div class="sub"><div class="lbl">Verify a download</div>',
           "<p>Check the file against the release checksums, from the folder you downloaded it to:</p>",
           copyline(f"sha256sum --ignore-missing -c checksums.txt"),
           "<p>On macOS use <code>shasum -a 256 --ignore-missing -c checksums.txt</code>. "
           "Each desktop file also has its own <code>.sha256</code> beside it.</p>"]
    if r.get("attested") and sample:
        out += ["<p>Check that GitHub Actions built it from this repository (needs the <code>gh</code> CLI):</p>",
                copyline(f"gh attestation verify {sample} --repo {REPO}")]
    else:
        out.append("<p>Build provenance attestations start with v1.8.0, so this release has checksums only.</p>")
    out.append("</div>")
    return "\n".join(out)


def contributors_html(rel):
    people = sorted({i["author"] for i in rel["items"] if i.get("author")}, key=str.lower)
    if not people:
        return ""
    links = ", ".join(f'<a href="https://github.com/{e(p)}">@{e(p)}</a>' for p in people)
    return (f'<section class="doc" id="contributors">\n<h2>Contributors</h2>\n'
            f'<p class="sum">Pull requests in this release came from {links}. Thank you.</p>\n</section>')


def highlights_html(notes):
    if not notes.get("highlights"):
        return ""
    return (f'<section class="doc" id="highlights">\n<h2>Highlights</h2>\n'
            f'<div class="sub md">{markdown(notes["highlights"])}</div>\n</section>')


HEADER = """<a class="skip-link" href="#main">Skip to content</a>
<header class="site-header" id="topbar">
  <a class="site-logo" href="/" aria-label="Hydra home">HYDRA</a>
  <span class="site-tag">releases</span>
  <button class="nav-toggle" type="button" aria-expanded="false" aria-controls="site-nav"><span class="nav-toggle-bars" aria-hidden="true"></span><span class="nav-toggle-label">Menu</span></button>
  <nav class="site-nav" id="site-nav" aria-label="Main">
    <a href="/#start">Get started</a>
    <a href="/docs.html">Docs</a>
    <a href="/pricing.html">Pricing</a>
    <a href="/first-principles.html">First principles</a>
    <a href="/blog/">Blog</a>
    <a href="https://github.com/ankit373/hydra">GitHub</a>
  </nav>
  <a class="btn btn-primary header-cta" href="/app.html">Desktop app</a>
</header>"""

FOOTER = """<footer class="site-footer">
  <div class="wrap">
    <div>
      <a class="site-logo" href="/" aria-label="Hydra home">HYDRA</a>
      <p>Hydra picks the model best suited to each task, and can check the answer before you rely on it. Free and open source, MIT licensed.</p>
    </div>
    <nav aria-label="Footer">
      <a href="/">Home</a>
      <a href="/docs.html">Docs</a>
      <a href="/app.html">Desktop app</a>
      <a href="/pricing.html">Pricing</a>
      <a href="/first-principles.html">First principles</a>
      <a href="/blog/">Blog</a>
      <a href="/releases/">Releases</a>
      <a href="https://github.com/ankit373/hydra">GitHub</a>
      <a href="/llms.txt">llms.txt</a>
    </nav>
  </div>
</footer>"""


def page(title, desc, path, jsonld, side, body):
    url = SITE + path
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
  <!-- Generated by docs/releases/generate.py from CHANGELOG.md, data/ and notes/. Do not edit. -->
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{e(title)}</title>
  <meta name="description" content="{e(desc)}">
  <meta property="og:type" content="article">
  <meta property="og:url" content="{url}">
  <meta property="og:title" content="{e(title)}">
  <meta property="og:description" content="{e(desc)}">
  <meta property="og:image" content="{SITE}/logo.png">
  <meta name="twitter:card" content="summary_large_image">
  <link rel="canonical" href="{url}">
  <link rel="icon" type="image/png" href="/logo.png">
  <link rel="ai-context" href="/llms.txt" type="text/plain">
  <link rel="alternate" type="application/atom+xml" title="Hydra releases" href="/releases/feed.xml">
  <script type="application/ld+json">
  {json.dumps(jsonld, ensure_ascii=False)}
  </script>

<script>try{{var t=localStorage.getItem('hydra-theme');if(t!=='system')document.documentElement.dataset.theme=t==='light'?'light':'dark'}}catch(e){{document.documentElement.dataset.theme='dark'}}</script>
<link rel="stylesheet" href="../assets/tokens.css">
<link rel="stylesheet" href="../assets/site.css">
<link rel="stylesheet" href="../assets/docs.css">
<link rel="stylesheet" href="../assets/releases.css">
<script src="../assets/site.js"></script>
</head>
<body>

{HEADER}

<div class="shell">
{side}
  <main class="content" id="main">
{body}
  </main>
</div>

{FOOTER}

</body>
</html>
"""


ACTIVE = ' class="active" aria-current="page"'


def side_html(releases, current):
    links = "\n".join(
        f'      <a href="/releases/v{r["version"]}.html"{ACTIVE if r["version"] == current else ""}>'
        f'v{r["version"]} <span class="when">{human_date(r["date"])}</span></a>'
        for r in releases)
    idx = ACTIVE if current is None else ""
    return f"""  <aside class="side rel-side">
    <nav aria-label="Releases">
      <div class="grp">Releases</div>
      <a href="/releases/"{idx}>All releases</a>
      <a href="/releases/feed.xml">Atom feed</a>
      <div class="grp">Versions</div>
{links}
    </nav>
  </aside>"""


def meta_pills(rel, prev):
    v = rel["version"]
    pills = [f'<span class="mp"><b>{human_date(rel["date"])}</b></span>',
             f'<span class="mp">{bump(v, prev)} release</span>',
             f'<span class="mp">{count_line(rel)}</span>',
             f'<a class="mp" href="{GH}/releases/tag/v{v}">GitHub release</a>']
    if prev:
        pills.append(f'<a class="mp" href="{GH}/compare/v{prev}...v{v}">Compare with v{prev}</a>')
    return '<div class="pill-row">' + "".join(pills) + "</div>"


def release_page(releases, idx):
    rel = releases[idx]
    prev = releases[idx + 1]["version"] if idx + 1 < len(releases) else None
    v = rel["version"]
    data, notes = rel["data"], rel["notes"]
    text, generated = summary(rel, notes)
    mark = ('\n    <p class="callout">This summary was generated from the changelog. '
            'No hand-written notes were kept for this release.</p>') if generated else ""
    toc_ids = [("highlights", "Highlights", notes.get("highlights"))]
    toc_ids += [(k, KIND_TITLES[k], counts(rel).get(k)) for k in KIND_TITLES]
    toc_ids += [("upgrade", "Upgrade notes", True), ("security", "Security", True),
                ("downloads", "Download and verify", True)]
    toc = '<nav class="page-toc" aria-label="On this page">' + "".join(
        f'<a href="#{i}">{t}</a>' for i, t, on in toc_ids if on) + "</nav>"
    body = f"""    <p class="crumb"><a href="/releases/">All releases</a></p>
    <h1>Hydra {v}</h1>
    <p class="intro">{e(text)}</p>{mark}
    {meta_pills(rel, prev)}
    {toc}
{highlights_html(notes)}
{kind_section(rel, "new", "new")}
{kind_section(rel, "fixed", "fixed")}
{kind_section(rel, "faster", "faster")}
{kind_section(rel, "changed", "changed")}
<section class="doc" id="upgrade">
<h2>Upgrade notes</h2>
{upgrade_html(rel, notes)}
</section>
<section class="doc" id="security">
<h2>Security</h2>
{security_html(data, notes)}
</section>
<section class="doc" id="downloads">
<h2>Download and verify</h2>
<p class="sum">Most people upgrade with <code>hyctl upgrade</code>, <code>brew upgrade hyctl</code>, npm or pip; see <a href="/docs.html#install">install</a>. The files below are for a manual install.</p>
{downloads_html(rel, data)}
</section>
{contributors_html(rel)}"""
    body = re.sub(r"\n{2,}", "\n", body)
    jsonld = {"@context": "https://schema.org", "@type": "TechArticle", "headline": f"Hydra {v} release notes",
              "url": f"{SITE}/releases/v{v}.html", "datePublished": rel["date"], "description": text,
              "publisher": {"@type": "Organization", "name": "Hydra", "url": f"{SITE}/"}}
    return page(f"Hydra {v} release notes", text, f"/releases/v{v}.html", jsonld,
                side_html(releases, v), body)


def index_entry(rel, prev):
    v = rel["version"]
    data, notes = rel["data"], rel["notes"]
    text, generated = summary(rel, notes)
    mark = ' <span class="refs">(generated from the changelog)</span>' if generated else ""
    lists = []
    for k in KIND_TITLES:
        items = [i for i in rel["items"] if i["kind"] == k]
        if not items:
            continue
        shown = "".join(item_html(i) for i in items[:4])
        more = (f'<li class="more-row"><a href="/releases/v{v}.html#{k}">and {len(items) - 4} more</a></li>'
                if len(items) > 4 else "")
        lists.append(f'<div class="sub"><div class="lbl">{KIND_TITLES[k]}</div><ul class="items">{shown}{more}</ul></div>')
    has_up = notes.get("upgrade notes") or any(i.get("upgrade") for i in rel["items"])
    up_line = f'<a href="/releases/v{v}.html#upgrade">Read the upgrade notes</a>' if has_up else "None written down."
    advs = data.get("advisories") or []
    if advs:
        sec_line = ", ".join(f'<a href="{e(a["url"])}">{e(a["id"])}</a>' for a in advs)
    elif notes.get("security"):
        sec_line = f'<a href="/releases/v{v}.html#security">Read the security notes</a>'
    else:
        sec_line = "No published advisory."
    dl = (f'<a class="more" href="/releases/v{v}.html#downloads">Download and verify</a>' if data.get("release")
          else f'<a class="more" href="{GH}/releases/tag/v{v}">Release page</a>')
    return f"""<section class="doc" id="v{v}">
<h2><a href="/releases/v{v}.html">Hydra {v}</a> <span class="tagline">{human_date(rel["date"])} · {bump(v, prev)}</span></h2>
<p class="sum">{e(text)}{mark}</p>
{"".join(lists)}
<div class="sub"><div class="lbl">Upgrade notes</div><p>{up_line}</p></div>
<div class="sub"><div class="lbl">Security</div><p>{sec_line}</p></div>
<p class="links"><a class="more" href="/releases/v{v}.html">Full notes for {v}</a>{dl}</p>
</section>"""


def index_page(releases):
    entries = "\n".join(
        index_entry(r, releases[i + 1]["version"] if i + 1 < len(releases) else None) for i, r in enumerate(releases))
    body = f"""    <h1>Releases</h1>
    <p class="intro">What changed in each Hydra release, newest first: what is new, what was fixed, what to do when you upgrade, and how to check the files you download. Every page is built from the changelog and GitHub, so it matches what shipped. Follow along with the <a href="/releases/feed.xml">Atom feed</a>.</p>
{entries}"""
    latest = releases[0]
    jsonld = {"@context": "https://schema.org", "@type": "CollectionPage", "name": "Hydra releases",
              "url": f"{SITE}/releases/", "dateModified": latest["date"],
              "hasPart": [{"@type": "TechArticle", "headline": f"Hydra {r['version']} release notes",
                           "url": f"{SITE}/releases/v{r['version']}.html", "datePublished": r["date"]} for r in releases]}
    return page("Hydra releases", "Every Hydra release in plain words: what is new, what was fixed, upgrade notes, security and verified downloads.",
                "/releases/", jsonld, side_html(releases, None), body)


def feed(releases):
    entries = []
    for r in releases[:20]:
        v = r["version"]
        text, _ = summary(r, r["notes"])
        url = f"{SITE}/releases/v{v}.html"
        entries.append(f"""  <entry>
    <title>Hydra {v}</title>
    <id>{url}</id>
    <link href="{url}"/>
    <updated>{r["date"]}T00:00:00Z</updated>
    <summary>{e(text)}</summary>
  </entry>""")
    return f"""<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Hydra releases</title>
  <id>{SITE}/releases/</id>
  <link href="{SITE}/releases/"/>
  <link rel="self" href="{SITE}/releases/feed.xml"/>
  <updated>{releases[0]["date"]}T00:00:00Z</updated>
  <author><name>Hydra</name></author>
{chr(10).join(entries)}
</feed>
"""


def sitemap_urls(releases):
    rows = [(f"{SITE}/releases/", releases[0]["date"], "weekly", "0.8")]
    rows += [(f"{SITE}/releases/v{r['version']}.html", r["date"], "monthly", "0.6") for r in releases]
    return "\n" + "\n".join(f"""  <url>
    <loc>{loc}</loc>
    <lastmod>{mod}</lastmod>
    <changefreq>{freq}</changefreq>
    <priority>{prio}</priority>
  </url>""" for loc, mod, freq, prio in rows) + "\n  "


def replace_block(text, name, body):
    pattern = re.compile(rf"(<!-- GENERATED:{name} -->)(.*?)(<!-- /GENERATED:{name} -->)", re.DOTALL)
    if not pattern.search(text):
        raise SystemExit(f"marker GENERATED:{name} not found")
    return pattern.sub(lambda m: m.group(1) + body + m.group(3), text)


def render():
    releases = [enrich(r) for r in changelog()]
    out = {HERE / "index.html": index_page(releases), HERE / "feed.xml": feed(releases)}
    for i, r in enumerate(releases):
        out[HERE / f"v{r['version']}.html"] = release_page(releases, i)
    sm = DOCS / "sitemap.xml"
    out[sm] = replace_block(sm.read_text(), "RELEASE-URLS", sitemap_urls(releases))
    known = set(out)
    stale = [p for p in HERE.glob("v*.html") if p not in known]
    return out, stale


def main(argv):
    if argv[:1] == ["fetch"]:
        fetch(argv[1:])
        return 0
    out, stale = render()
    changed = [p for p, s in out.items() if not p.exists() or p.read_text() != s]
    if "--check" in argv:
        bad = changed + stale
        if bad:
            print("out of date, run python3 docs/releases/generate.py:", *[str(p.relative_to(REPO_ROOT)) for p in bad])
            return 1
        return 0
    for p in changed:
        p.write_text(out[p])
        print(f"wrote {p.relative_to(REPO_ROOT)}")
    for p in stale:
        p.unlink()
        print(f"removed {p.relative_to(REPO_ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
