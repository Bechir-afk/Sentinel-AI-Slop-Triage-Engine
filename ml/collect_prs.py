"""Task 2: scrape raw PRs from GitHub into JSONL for later labeling.

Pulls two buckets via the REST API:
  LEGIT candidates -> merged PRs from active real repos.
  SLOP  candidates -> closed-unmerged PRs carrying spam/invalid labels (scarce).

Output rows: {title, diff, source, repo, number}. Labeling happens later in
build_dataset.py (real PRs need human review; see PROJECT_MAP DATA PIPELINE).
Not in the serving path — run offline, once.

Usage:
    GITHUB_TOKEN=... python collect_prs.py --repos owner/repo,owner2/repo2 --out raw_prs.jsonl
"""

import argparse
import json
import logging
import os
import time

import requests

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.collect")

API = "https://api.github.com"
SPAM_LABELS = {"spam", "invalid", "wontfix", "spam-or-invalid"}


def _session(token: str) -> requests.Session:
    s = requests.Session()
    s.headers.update(
        {
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        }
    )
    return s


def _fetch_diff(s: requests.Session, repo: str, number: int) -> str:
    """Fetch the unified diff for one PR (same endpoint the gateway uses)."""
    r = s.get(
        f"{API}/repos/{repo}/pulls/{number}",
        headers={"Accept": "application/vnd.github.v3.diff"},
        timeout=30,
    )
    if r.status_code != 200:
        logger.warning("diff %s#%d: status %d", repo, number, r.status_code)
        return ""
    return r.text


def _labels(pr: dict) -> set:
    return {lbl["name"].lower() for lbl in pr.get("labels", [])}


def collect(s: requests.Session, repo: str, per_repo: int):
    """Yield candidate rows from one repo's closed PRs."""
    page = 1
    seen = 0
    while seen < per_repo:
        r = s.get(
            f"{API}/repos/{repo}/pulls",
            params={"state": "closed", "per_page": 100, "page": page},
            timeout=30,
        )
        if r.status_code != 200:
            logger.warning("list %s page %d: status %d", repo, page, r.status_code)
            return
        batch = r.json()
        if not batch:
            return
        for pr in batch:
            merged = pr.get("merged_at") is not None
            spam = bool(_labels(pr) & SPAM_LABELS)
            if not merged and not spam:
                continue  # ambiguous closed PR — skip, don't guess a label
            diff = _fetch_diff(s, repo, pr["number"])
            if not diff:
                continue
            yield {
                "title": pr.get("title", ""),
                "diff": diff,
                "source": "merged" if merged else "spam-label",
                "repo": repo,
                "number": pr["number"],
            }
            seen += 1
            time.sleep(0.5)  # stay polite with secondary rate limits
            if seen >= per_repo:
                return
        page += 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repos", required=True, help="comma-separated owner/repo list")
    ap.add_argument("--out", default="raw_prs.jsonl")
    ap.add_argument("--per-repo", type=int, default=200)
    args = ap.parse_args()

    token = os.environ["GITHUB_TOKEN"]
    s = _session(token)

    n = 0
    with open(args.out, "w", encoding="utf-8") as f:
        for repo in [r.strip() for r in args.repos.split(",") if r.strip()]:
            logger.info("collecting %s", repo)
            for row in collect(s, repo, args.per_repo):
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
                n += 1
    logger.info("wrote %d rows to %s", n, args.out)


if __name__ == "__main__":
    main()
