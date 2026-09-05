"""Close the issues whose PRs shipped in a release. Driven by close-shipped-issues.yml.

Reads shipped_prs.txt (the numbers in the release's commit subjects), works out what
each one closes, and closes those issues. GitHub's own link API is empty for
develop-targeted PRs, so a PR body is the only surviving record of what it closes.
"""

import json
import os
import re
import subprocess
import sys
import time

REPO = os.environ["REPO"]
TAG = os.environ["TAG"]
DRY_RUN = os.environ.get("DRY_RUN", "") == "true"
OWNER, NAME = REPO.split("/")

# The keyword set GitHub itself honours. Anchored on a word boundary so "for #517"
# stays a plain reference and only an actual closing keyword closes anything.
CLOSES = re.compile(
    r"\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s*#(\d+)\b", re.IGNORECASE
)
BATCH = 50


def graphql(query):
    """A number that exists as neither issue nor PR errors for that alias alone; gh
    exits 1 but still prints the rest, which is what we want. A batch where every
    alias came back null is not that, it means the query shape is wrong, so fail."""
    out = subprocess.run(
        ["gh", "api", "graphql", "-f", "query=" + query],
        capture_output=True, text=True,
    ).stdout
    payload = json.loads(out) if out.strip() else {}
    repo = (payload.get("data") or {}).get("repository")
    if repo is None:
        sys.exit("GraphQL returned no repository data: " + out[:500])
    nodes = [node for node in repo.values() if node]
    if repo and not nodes:
        sys.exit("GraphQL resolved none of %d aliases: %s" % (len(repo), out[:500]))
    return nodes


def batched(numbers, field):
    for i in range(0, len(numbers), BATCH):
        chunk = numbers[i:i + BATCH]
        aliases = " ".join("n%d: %s" % (n, field % n) for n in chunk)
        yield from graphql('{ repository(owner:"%s", name:"%s") { %s } }' % (OWNER, NAME, aliases))


def main():
    with open("shipped_prs.txt") as fh:
        shipped = sorted({int(line) for line in fh if line.strip()})
    print("Numbers in the release's commit subjects: %d" % len(shipped))

    # A trailing (#n) is not always a PR. GitHub's squash default appends the PR
    # number, but this repo's own commit convention appends the ISSUE number
    # (CLAUDE.md: `git commit -m "feat(stats): ... (#${ISSUE})"`), and both are in
    # the history, so resolve what the number actually is and handle each.
    query = ("issueOrPullRequest(number:%d){ __typename "
             "... on Issue { number } "
             "... on PullRequest { number body merged } }")
    via = {}
    for node in batched(shipped, query):
        if node["__typename"] == "Issue":
            # The commit names the issue it implements: it shipped, no body to read.
            via.setdefault(node["number"], set()).add("commit subject")
        elif node["merged"]:
            for issue in CLOSES.findall(node["body"] or ""):
                via.setdefault(int(issue), set()).add("#%d" % node["number"])
    print("Referenced issue numbers: %d" % len(via))

    # A closing keyword can name a PR, or an issue someone already closed. Only an
    # OPEN Issue is touched, which is also what makes a re-run a no-op.
    query = ("issueOrPullRequest(number:%d){ __typename "
             "... on Issue { number title state } }")
    targets = [n for n in batched(sorted(via), query)
               if n.get("__typename") == "Issue" and n["state"] == "OPEN"]

    summary = ["### Close shipped issues, `%s`" % TAG, ""]
    if not targets:
        print("Nothing to close: every referenced issue is already closed.")
        summary.append("Nothing to close, every issue in this release is already closed.")
    else:
        summary.append("| Issue | Shipped by | Title |")
        summary.append("|---|---|---|")

    failures = []
    for issue in targets:
        num = issue["number"]
        source = ", ".join(sorted(via[num]))
        if DRY_RUN:
            print("DRY RUN would close #%d (via %s), %s" % (num, source, issue["title"]))
        else:
            done = subprocess.run(
                ["gh", "issue", "close", str(num), "--repo", REPO,
                 "--comment", "Shipped in %s." % TAG],
                capture_output=True, text=True,
            )
            if done.returncode != 0:
                failures.append("#%d: %s" % (num, done.stderr.strip()))
                continue
            print("Closed #%d (via %s)" % (num, source))
            time.sleep(1)  # stay under GitHub's content-creation secondary rate limit
        summary.append("| #%d | %s | %s |" % (num, source, issue["title"]))

    summary.append("")
    summary.append("%d issue(s)%s." % (len(targets), ", dry run, nothing closed" if DRY_RUN else " closed"))
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if path:
        with open(path, "a") as fh:
            fh.write("\n".join(summary) + "\n")

    if failures:
        sys.exit("Failed to close:\n  " + "\n  ".join(failures))


if __name__ == "__main__":
    main()
