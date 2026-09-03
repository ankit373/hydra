"""Close the issues whose PRs shipped in a release. Driven by close-shipped-issues.yml.

Reads shipped_prs.txt (PR numbers in the release), recovers `Closes #n` from each PR
body, and closes those issues. GitHub's own link API is empty for develop-targeted
PRs, so the body is the only surviving record of what a PR closes.
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
    """A number that is not a PR errors for that alias alone; gh exits 1 but still
    prints the rest, which is what we want. Only a wholly empty reply is fatal."""
    out = subprocess.run(
        ["gh", "api", "graphql", "-f", "query=" + query],
        capture_output=True, text=True,
    ).stdout
    payload = json.loads(out) if out.strip() else {}
    repo = (payload.get("data") or {}).get("repository")
    if repo is None:
        sys.exit("GraphQL returned no repository data: " + out[:500])
    return [node for node in repo.values() if node]


def batched(numbers, field):
    for i in range(0, len(numbers), BATCH):
        chunk = numbers[i:i + BATCH]
        aliases = " ".join("n%d: %s" % (n, field % n) for n in chunk)
        yield from graphql('{ repository(owner:"%s", name:"%s") { %s } }' % (OWNER, NAME, aliases))


def main():
    with open("shipped_prs.txt") as fh:
        shipped = sorted({int(line) for line in fh if line.strip()})
    print("Shipped PRs: %d" % len(shipped))

    closes = {}
    for pr in batched(shipped, "pullRequest(number:%d){ number body merged }"):
        if not pr["merged"]:
            continue
        for issue in CLOSES.findall(pr["body"] or ""):
            closes.setdefault(int(issue), set()).add(pr["number"])
    print("Referenced issue numbers: %d" % len(closes))

    # A closing keyword can name a PR, or an issue someone already closed. Only an
    # OPEN Issue is touched, which is also what makes a re-run a no-op.
    query = ("issueOrPullRequest(number:%d){ __typename "
             "... on Issue { number title state } }")
    targets = [n for n in batched(sorted(closes), query)
               if n.get("__typename") == "Issue" and n["state"] == "OPEN"]

    summary = ["### Close shipped issues — `%s`" % TAG, ""]
    if not targets:
        print("Nothing to close: every referenced issue is already closed.")
        summary.append("Nothing to close — every issue in this release is already closed.")
    else:
        summary.append("| Issue | Shipped by | Title |")
        summary.append("|---|---|---|")

    failures = []
    for issue in targets:
        num = issue["number"]
        via = ", ".join("#%d" % p for p in sorted(closes[num]))
        if DRY_RUN:
            print("DRY RUN would close #%d (via %s) — %s" % (num, via, issue["title"]))
        else:
            done = subprocess.run(
                ["gh", "issue", "close", str(num), "--repo", REPO,
                 "--comment", "Shipped in %s." % TAG],
                capture_output=True, text=True,
            )
            if done.returncode != 0:
                failures.append("#%d: %s" % (num, done.stderr.strip()))
                continue
            print("Closed #%d (via %s)" % (num, via))
            time.sleep(1)  # stay under GitHub's content-creation secondary rate limit
        summary.append("| #%d | %s | %s |" % (num, via, issue["title"]))

    summary.append("")
    summary.append("%d issue(s)%s." % (len(targets), " — dry run, nothing closed" if DRY_RUN else " closed"))
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if path:
        with open(path, "a") as fh:
            fh.write("\n".join(summary) + "\n")

    if failures:
        sys.exit("Failed to close:\n  " + "\n  ".join(failures))


if __name__ == "__main__":
    main()
