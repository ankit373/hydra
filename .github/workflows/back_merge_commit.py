#!/usr/bin/env python3
"""Publish the cherry-picked back-merge as ONE commit GitHub itself signs.

A commit pushed over git from a runner is unsigned, and `develop` has
required_signatures with enforce_admins, so such a PR can never merge however
many signed commits are stacked on top of it: the unsigned one is still in the
PR. createCommitOnBranch is the only way a workflow can author a signed commit,
so the branch is reset to develop and the whole cherry-pick result is replayed
onto it as a single signed commit.
"""
import base64, json, os, subprocess, sys, urllib.request

API = "https://api.github.com"
REPO = os.environ["GITHUB_REPOSITORY"]
TOKEN = os.environ["GH_TOKEN"]
BRANCH = os.environ.get("BACK_MERGE_BRANCH", "chore/back-merge-main")


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def call(url, payload=None, method=None):
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode() if payload is not None else None,
        method=method or ("POST" if payload is not None else "GET"),
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req) as r:
            return json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        # The status alone does not say which of several conditions fired; GitHub
        # puts that in the body, and without it a 422 is unreadable from the log.
        e.body = (e.read() or b"").decode(errors="replace")
        print(f"{method or 'GET'} {url} -> {e.code}: {e.body}", file=sys.stderr)
        raise


def main():
    base = run("git", "rev-parse", "origin/develop").strip()

    # Point the branch at develop, so the signed commit below is the only thing on
    # it. Update-then-create rather than delete-then-create: GitHub answers a
    # DELETE for a missing ref with 422 "Reference does not exist", never 404, so
    # the first version died whenever the branch had been auto-deleted by its own
    # merge, and fell back to the unsigned push this script exists to avoid. PATCH
    # answers 422 the same way, so the fallback is on the condition, not the code.
    try:
        call(f"{API}/repos/{REPO}/git/refs/heads/{BRANCH}",
             {"sha": base, "force": True}, method="PATCH")
    except urllib.error.HTTPError as e:
        if e.code not in (404, 422):
            raise
        call(f"{API}/repos/{REPO}/git/refs",
             {"ref": f"refs/heads/{BRANCH}", "sha": base})

    # Whatever the cherry-pick actually produced, so a hotfix's files ride along
    # rather than only the release manifest.
    # --no-renames keeps every record two fields wide. With detection on, a
    # rename emits R<score>, old, new, and a parser reading pairs would take the
    # new path as the next status code and mis-assign everything after it.
    status = run("git", "diff", "--name-status", "-z", "--no-renames",
                 f"{base}..HEAD").split("\0")
    additions, deletions = [], []
    i = 0
    while i < len(status) - 1:
        code, path, i = status[i], status[i + 1], i + 2
        if not code:
            break
        if code[0] not in "AMDT":
            raise SystemExit(f"unexpected git status code {code!r} for {path!r}")
        if code.startswith("D"):
            deletions.append({"path": path})
        else:
            blob = subprocess.run(["git", "show", f"HEAD:{path}"],
                                  check=True, capture_output=True).stdout
            additions.append({"path": path,
                              "contents": base64.b64encode(blob).decode()})

    if not additions and not deletions:
        print("nothing to commit")
        return 0

    subject = run("git", "log", "-1", "--format=%s").strip()
    body = ("Automated GitFlow back-merge, replayed onto develop as one commit so "
            "GitHub signs it. A git-pushed runner commit is unsigned, and develop "
            "requires signatures with no admin bypass.")
    res = call(f"{API}/graphql", {
        "query": """
        mutation($input: CreateCommitOnBranchInput!) {
          createCommitOnBranch(input: $input) { commit { oid } }
        }""",
        "variables": {"input": {
            "branch": {"repositoryNameWithOwner": REPO, "branchName": BRANCH},
            "message": {"headline": f"chore: back-merge main → develop ({subject})"[:72],
                        "body": body},
            "expectedHeadOid": base,
            "fileChanges": {"additions": additions, "deletions": deletions},
        }},
    })
    if res.get("errors"):
        print("createCommitOnBranch failed:", json.dumps(res["errors"]), file=sys.stderr)
        return 1

    oid = res["data"]["createCommitOnBranch"]["commit"]["oid"]
    print(f"signed back-merge commit {oid} ({len(additions)} changed, {len(deletions)} deleted)")
    with open(os.environ["GITHUB_OUTPUT"], "a") as fh:
        fh.write(f"head={oid}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
