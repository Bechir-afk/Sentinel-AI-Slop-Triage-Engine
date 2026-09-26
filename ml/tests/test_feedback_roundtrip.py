"""Self-check for the feedback fold-in round-trip (T014, FR-008/FR-016, SC-006).

The gateway writes an identity-only maintainer-disagreement row (no diff body,
FR-014). This proves the offline half: build_dataset.py --feedback-log resolves
that row to (title, diff) by PR identity and folds it into the dataset as a
LEGIT example (the maintainer overturned our slop verdict), with the cross-split
leakage invariant still holding.

Pure stdlib — no torch, no pandas, no requests, no network. We register a fake
`collect_prs` in sys.modules so build_dataset's deferred `import collect_prs`
picks it up; the fake maps PR identity -> canned (title, diff), standing in for
the GitHub REST calls the real fold-in makes. This mirrors the black-box stub
approach the Go harness uses (T003), one layer down.

Run: python ml/tests/test_feedback_roundtrip.py   (exit 0 = pass)
"""

import json
import os
import sys
import tempfile
import types

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import build_dataset as bd  # noqa: E402

# The one PR named by our feedback row. build_dataset resolves identity -> content
# through the (stubbed) GitHub client; the row itself never carries this diff.
_PR_REPO, _PR_NUM = "octo/repo", 7
_PR_TITLE = "Legit refactor that a maintainer un-flagged"
_PR_DIFF = (
    "diff --git a/real.py b/real.py\n"
    "--- a/real.py\n"
    "+++ b/real.py\n"
    "@@ -1,3 +1,4 @@\n"
    " def compute(x):\n"
    "+    x = validate(x)\n"
    "     return x * 2\n"
)


class _FakeResp:
    def __init__(self, status, payload):
        self.status_code = status
        self._payload = payload

    def json(self):
        return self._payload


class _FakeSession:
    """Stands in for requests.Session — only .get is exercised (for the title)."""

    def get(self, url, timeout=None):
        # url shape: {API}/repos/{owner}/{repo}/pulls/{number}
        tail = url.split("/repos/", 1)[1]
        repo, _, num = tail.rpartition("/pulls/")
        if (repo, int(num)) == (_PR_REPO, _PR_NUM):
            return _FakeResp(200, {"title": _PR_TITLE})
        return _FakeResp(404, {})


def _install_fake_github():
    """Register a fake `collect_prs` so the deferred import inside
    build_dataset._load_feedback resolves to our in-memory stub, not the real
    (requests-backed, network-hitting) module. Returns the previous binding so
    the caller can restore it."""
    fake = types.ModuleType("collect_prs")
    fake.API = "https://fake.github"
    fake._session = lambda token: _FakeSession()

    def _fetch_diff(s, repo, number):
        if (repo, number) == (_PR_REPO, _PR_NUM):
            return _PR_DIFF
        return ""  # unknown PR -> empty diff -> build_dataset skips it

    fake._fetch_diff = _fetch_diff
    prev = sys.modules.get("collect_prs")
    sys.modules["collect_prs"] = fake
    return prev


def _restore(prev):
    if prev is None:
        sys.modules.pop("collect_prs", None)
    else:
        sys.modules["collect_prs"] = prev


def _write_feedback_log(rows):
    """Write gateway-shaped JSONL rows (exactly what feedback.go emits) to a temp
    file and return its path."""
    fd, path = tempfile.mkstemp(suffix=".jsonl")
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")
    return path


def _feedback_row():
    # Identity-only, exactly as internal/webhook/feedback.go Signal serializes it:
    # no diff, no title, no confidence (unknown -> omitempty).
    return {
        "pr": f"{_PR_REPO}#{_PR_NUM}",
        "original_verdict": "slop",
        "disagreement_type": "label-removed",
        "ts": "2026-09-25T12:00:00Z",
    }


def test_feedback_row_folds_in_as_legit():
    prev = _install_fake_github()
    os.environ["GITHUB_TOKEN"] = "fake-token-not-used-for-network"  # _load_feedback requires it
    try:
        path = _write_feedback_log([_feedback_row()])
        try:
            rows = bd._load_feedback(path)
        finally:
            os.remove(path)
    finally:
        _restore(prev)

    assert len(rows) == 1, f"want exactly 1 folded row, got {len(rows)}: {rows}"
    row = rows[0]
    assert row["label"] == bd.LEGIT, "overturned slop verdict must fold in as LEGIT"
    assert row["title"] == _PR_TITLE, "title must be fetched by PR identity"
    assert row["diff"] == _PR_DIFF, "diff must be fetched by PR identity"
    print("ok: identity-only feedback row resolves to a LEGIT (title, diff) row")


def test_folded_row_enters_dataset_without_leakage():
    prev = _install_fake_github()
    os.environ["GITHUB_TOKEN"] = "fake-token-not-used-for-network"
    try:
        path = _write_feedback_log([_feedback_row()])
        try:
            feedback_rows = bd._load_feedback(path)
        finally:
            os.remove(path)
    finally:
        _restore(prev)

    # Mirror build_dataset.main(): feedback rows join the real pool, then split.
    raw = [{"title": f"real pr {i}", "diff": f"diff body {i}", "label": i % 2} for i in range(40)]
    splits = bd.build(raw + feedback_rows, synthetic_total=200, seed=42)  # raises on any leak

    # The folded PR is present exactly once, labeled LEGIT.
    target = bd._row_hash({"title": _PR_TITLE, "diff": _PR_DIFF})
    hits = [
        r
        for rows in splits.values()
        for r in rows
        if bd._row_hash(r) == target
    ]
    assert len(hits) == 1, f"folded PR should appear exactly once, found {len(hits)}"
    assert hits[0]["label"] == bd.LEGIT, "folded PR must be LEGIT in the built dataset"

    # Leakage invariant still holds with the feedback row present (SC-006 half 2).
    bd.assert_no_leakage(splits)
    print("ok: folded feedback row enters the dataset once, LEGIT, no leakage")


def test_missing_token_is_actionable():
    # Without GITHUB_TOKEN the fold-in must refuse clearly (it needs to fetch
    # title+diff), never silently drop the only production-learning signal.
    prev = _install_fake_github()
    os.environ.pop("GITHUB_TOKEN", None)
    try:
        path = _write_feedback_log([_feedback_row()])
        try:
            bd._load_feedback(path)
        except SystemExit as e:
            assert "GITHUB_TOKEN" in str(e), f"want actionable token message, got: {e}"
            print("ok: missing GITHUB_TOKEN fails with an actionable message")
            return
        finally:
            os.remove(path)
    finally:
        _restore(prev)
    raise AssertionError("_load_feedback did not refuse when GITHUB_TOKEN was unset")


if __name__ == "__main__":
    test_feedback_row_folds_in_as_legit()
    test_folded_row_enters_dataset_without_leakage()
    test_missing_token_is_actionable()
    print("all feedback round-trip self-checks passed")
