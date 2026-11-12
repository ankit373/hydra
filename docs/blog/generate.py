#!/usr/bin/env python3
"""Regenerate the marked sections of docs/blog/index.html and docs/sitemap.xml
from docs/blog/posts.json, the single source of truth for post metadata.

Adding a post: append an object to posts.json (see existing entries for the
shape), then run this script. Nothing else needs hand-editing. Adding a new
*topic* (not just a new post under an existing one) needs one line in TOPICS
below, picking its accent color deliberately.

Usage: python3 docs/blog/generate.py [--check]
  --check   exit 1 if regenerating would change either file, without writing
"""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent  # docs/
BLOG = ROOT / "blog"

TOPICS = {
    "security": ("Security research", "var(--t-security)"),
    "competitive": ("Competitive research", "var(--t-competitive)"),
    "frontier": ("Frontier AI", "var(--t-frontier)"),
    "engineering": ("Engineering", "var(--t-engineering)"),
}


def load_posts():
    posts = json.loads((BLOG / "posts.json").read_text())
    for p in posts:
        if p["topic"] not in TOPICS:
            raise SystemExit(f"{p['slug']}: unknown topic {p['topic']!r}, add it to TOPICS first")
    posts.sort(key=lambda p: p["date"], reverse=True)
    featured = [p for p in posts if p.get("featured")]
    if len(featured) != 1:
        raise SystemExit(f"expected exactly one featured post, found {len(featured)}")
    return posts, featured[0]


def render_jsonld(posts):
    lines = []
    for p in posts:
        headline = json.dumps(p["title"])
        lines.append(
            f'     {{"@type":"{p["schema_type"]}","headline":{headline},'
            f'"url":"https://hydra.uvansa.com{p["url"]}","datePublished":"{p["date"]}"}},'
        )
    lines[-1] = lines[-1].rstrip(",")
    return "\n" + "\n".join(lines) + "\n"


def render_ticker(posts):
    stats = [s for p in posts for s in p["ticker"]]
    spans = [f"      <span><i></i>{s}</span>" for s in stats] * 2
    return "\n" + "\n".join(spans) + "\n"


def render_filters(posts):
    used = [t for t in TOPICS if any(p["topic"] == t for p in posts)]
    lines = [
        f'    <button class="chip" data-topic="{t}" style="--chip-c:{TOPICS[t][1]}"><i></i>{TOPICS[t][0]}</button>'
        for t in used
    ]
    return "\n" + "\n".join(lines) + "\n"


def render_featured(p):
    kpis = "\n".join(
        f'      <div class="kpi"><div class="n">{n}</div><div class="t">{t}</div></div>' for n, t in p["kpis"]
    )
    label = TOPICS[p["topic"]][0].lower()
    return f"""
  <a class="featured" data-topic="{p['topic']}" href="{p['url']}">
    <div class="fk">featured · {label} · {p['date_human']}</div>
    <div class="ftitle">{p['title']}</div>
    <p class="fdek">{p['dek']}</p>
    <div class="kpis">
{kpis}
    </div>
    <div class="fmeta"><span>{p['read_min']} min read</span><span>·</span><span>{p['sources']} sources</span></div>
    <div class="read">Read the post →</div>
  </a>
"""


def render_grid(posts):
    cards = []
    for p in posts:
        if p.get("featured"):
            continue
        label, color = TOPICS[p["topic"]]
        cards.append(f"""    <a class="card" data-topic="{p['topic']}" style="--card-c:{color}" href="{p['url']}">
      <div class="meta"><span class="topic">{label}</span><span>·</span><span>{p['date_human']}</span></div>
      <div class="title">{p['title']}</div>
      <p class="dek">{p['dek']}</p>
      <div class="foot"><span class="read">{p['read_min']} min read →</span><span class="evid">{p['evidence']}</span></div>
    </a>""")
    return "\n" + "\n".join(cards) + "\n"


def render_sitemap_urls(posts):
    lines = []
    for p in posts:
        lines.append(f"""  <url>
    <loc>https://hydra.uvansa.com{p['url']}</loc>
    <lastmod>{p['lastmod']}</lastmod>
    <changefreq>monthly</changefreq>
    <priority>0.7</priority>
  </url>""")
    return "\n" + "\n".join(lines) + "\n  "


def replace_block(text, name, body):
    pattern = re.compile(
        rf"(<!-- GENERATED:{name} -->)(.*?)(<!-- /GENERATED:{name} -->)", re.DOTALL
    )
    if not pattern.search(text):
        raise SystemExit(f"marker GENERATED:{name} not found")
    return pattern.sub(lambda m: m.group(1) + body + m.group(3), text)


def main():
    check_only = "--check" in sys.argv
    posts, featured = load_posts()

    index_path = BLOG / "index.html"
    index = index_path.read_text()
    index = replace_block(index, "JSONLD", render_jsonld(posts))
    index = replace_block(index, "TICKER", render_ticker(posts))
    index = replace_block(index, "FILTERS", render_filters(posts))
    index = replace_block(index, "FEATURED", render_featured(featured))
    index = replace_block(index, "GRID", render_grid(posts))

    sitemap_path = ROOT / "sitemap.xml"
    sitemap = sitemap_path.read_text()
    sitemap = replace_block(sitemap, "BLOG-URLS", render_sitemap_urls(posts))

    changed = []
    if index != index_path.read_text():
        changed.append(index_path)
    if sitemap != sitemap_path.read_text():
        changed.append(sitemap_path)

    if check_only:
        if changed:
            print("out of date:", *[str(p) for p in changed])
            return 1
        print("up to date")
        return 0

    index_path.write_text(index)
    sitemap_path.write_text(sitemap)
    print(f"wrote {index_path}" if index_path in changed else f"{index_path} unchanged")
    print(f"wrote {sitemap_path}" if sitemap_path in changed else f"{sitemap_path} unchanged")
    return 0


if __name__ == "__main__":
    sys.exit(main())
