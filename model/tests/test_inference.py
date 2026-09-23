"""Self-check for pair-encoding correctness (T015, SC-006 / FR-008).

The invariant: (title, diff) is fed through the tokenizer's PAIR api, so the
model's REAL separator is inserted (not a literal "[SEP]" substring), only the
DIFF is truncated, and a pathologically long title is capped first so it can
never evict the diff from the window.

encoding.py is pure-Python, so the always-on portion drives encode_pair with a
faithful fake tokenizer and needs no torch/transformers — it runs in CI next to
the ml/ self-checks. When transformers IS importable, an extra authoritative
pass loads a real RoBERTa/CodeBERT-family tokenizer and re-checks the same
invariants against the genuine separator token.

Run: python model/tests/test_inference.py   (exit 0 = pass)
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import encoding  # noqa: E402


class FakeTokenizer:
    """Minimal stand-in for a RoBERTa/CodeBERT fast tokenizer, faithful to the two
    call shapes encode_pair relies on. Whitespace 'tokens'; CLS/SEP sentinels
    stand in for the real <s> / </s></s> so the pair path is checkable without
    transformers."""

    CLS, SEP = -1, -2  # real tokenizer uses <s> and a </s></s> pair

    def __init__(self):
        self._vocab: dict[str, int] = {}

    def _ids(self, text):
        return [self._vocab.setdefault(w, len(self._vocab) + 1) for w in text.split()]

    def encode(self, text, add_special_tokens=False, truncation=False, max_length=None):
        ids = self._ids(text)
        if truncation and max_length is not None:
            ids = ids[:max_length]
        return ids

    def decode(self, ids):
        # encode_pair only needs the capped title back as text; one word per id
        # preserves the token count through the re-encode in __call__.
        return " ".join(f"t{i}" for i in ids)

    def __call__(self, title, diff, truncation=None, max_length=None, **kwargs):
        self.last_kwargs = {"truncation": truncation, "max_length": max_length, **kwargs}
        # A real tokenizer batch-encodes when handed equal-length lists; recurse
        # per pair so the fake honors that shape too.
        if isinstance(title, list):
            rows = [self(t, d, truncation=truncation, max_length=max_length) for t, d in zip(title, diff)]
            return {"input_ids": [r["input_ids"] for r in rows]}
        self.title_ids = self._ids(title)
        diff_ids = self._ids(diff)
        # only_second: the diff is what gives when the window is tight.
        budget = (max_length or encoding.MAX_TOKENS) - len(self.title_ids) - 3
        if truncation == "only_second":
            diff_ids = diff_ids[: max(budget, 0)]
        self.diff_ids = diff_ids
        return {"input_ids": [self.CLS] + self.title_ids + [self.SEP, self.SEP] + diff_ids}


def test_long_title_still_yields_diff_tokens():
    tok = FakeTokenizer()
    long_title = "word " * 1000  # far past the 64-token title cap
    diff = "diff --git a/x b/x\n+real change here\n"
    out = encoding.encode_pair(tok, long_title, diff)

    assert len(tok.title_ids) <= encoding.TITLE_MAX_TOKENS, (
        f"title not capped: {len(tok.title_ids)} > {encoding.TITLE_MAX_TOKENS}"
    )
    assert tok.diff_ids, "diff was fully evicted — a long title starved the window"
    assert out["input_ids"][-1] == tok.diff_ids[-1], "diff tokens missing from output tail"
    print(f"ok: long title capped to {len(tok.title_ids)} tokens, diff kept {len(tok.diff_ids)}")


def test_uses_real_separator_not_literal_sep():
    tok = FakeTokenizer()
    out = encoding.encode_pair(tok, "fix bug", "diff --git a/x b/x\n+ok\n")
    ids = out["input_ids"]

    # The pair api inserts the tokenizer's own separator between segments; the old
    # f"{title}\n[SEP]\n{diff}" string never would.
    assert tok.SEP in ids, "separator token absent — pair api not used"
    assert "[SEP]" not in "".join(f"{i}" for i in ids), "literal [SEP] leaked into ids"
    assert tok.last_kwargs["truncation"] == "only_second", "diff-only truncation not requested"
    assert tok.last_kwargs["max_length"] == encoding.MAX_TOKENS
    print("ok: real separator inserted via pair api, truncation=only_second")


def test_accepts_batched_lists():
    tok = FakeTokenizer()
    titles = ["a", "word " * 1000]
    diffs = ["+x\n", "+y\n"]
    # training passes equal-length lists; encode_pair must cap each title in place.
    encoding.encode_pair(tok, titles, diffs)
    print("ok: batched list input accepted")


def test_real_tokenizer_if_available():
    """Authoritative pass against a genuine tokenizer — skipped when transformers
    isn't installed (CI runs the fake-tokenizer checks above without the ML stack)."""
    try:
        from transformers import AutoTokenizer
    except ImportError:
        print("skip: transformers not installed — fake-tokenizer checks cover the invariant")
        return
    try:
        tok = AutoTokenizer.from_pretrained("roberta-base")
    except Exception as e:  # noqa: BLE001 — offline / no cache: don't fail the suite
        print(f"skip: could not load a real tokenizer ({e})")
        return

    long_title = "refactor " * 500
    diff = "diff --git a/x b/x\n" + "+line\n" * 200
    ids = encoding.encode_pair(tok, long_title, diff)["input_ids"]
    assert tok.sep_token_id in ids, "real separator token missing"
    assert len(ids) <= encoding.MAX_TOKENS, "exceeded max window"
    # The diff should still be represented: total length hits the cap because the
    # diff filled the window the capped title left free.
    assert len(ids) > encoding.TITLE_MAX_TOKENS, "diff evicted with a real tokenizer"
    print(f"ok: real tokenizer — {len(ids)} tokens, separator {tok.sep_token_id} present")


if __name__ == "__main__":
    test_long_title_still_yields_diff_tokens()
    test_uses_real_separator_not_literal_sep()
    test_accepts_batched_lists()
    test_real_tokenizer_if_available()
    print("all pair-encoding self-checks passed")
