"""Self-check for threshold selection (T014, FR-010). numpy-only — no torch/
transformers — so it runs in CI without the training stack.

Run: python ml/tests/test_thresholds.py   (exit 0 = pass)
"""

import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from thresholds import select_threshold, softmax  # noqa: E402


def test_softmax_rows_sum_to_one():
    logits = np.array([[2.0, 1.0], [-1.0, 3.0], [0.0, 0.0]])
    probs = softmax(logits)
    assert np.allclose(probs.sum(axis=-1), 1.0), "softmax rows must sum to 1"
    assert probs[0, 0] > probs[0, 1], "higher logit -> higher prob"
    print("ok: softmax normalized")


def test_picks_lowest_threshold_clearing_bar():
    # SLOP items (label 1) score high; LEGIT (label 0) score low, with one
    # borderline legit at 0.80 that a too-low threshold would misflag.
    probs = [0.95, 0.90, 0.88, 0.82, 0.10, 0.20, 0.80]
    y = [1, 1, 1, 1, 0, 0, 0]
    # 0.81 is the lowest candidate that still excludes the borderline 0.80 legit
    # (0.80 < 0.81) -> flags 4 slop, 0 fp, precision 1.0.
    # At t=0.80: also flags the 0.80 legit -> 4 tp, 1 fp -> precision 0.8 < 0.85.
    t, prec = select_threshold(probs, y, bar=0.85)
    assert t == 0.81, f"expected lowest clearing threshold 0.81, got {t}"
    assert prec >= 0.85, f"selected precision {prec} below bar"
    print(f"ok: picked lowest clearing threshold {t} (precision {prec:.2f})")


def test_prefers_lower_threshold_when_multiple_clear():
    # All SLOP score very high, no LEGIT near them: precision is 1.0 across a
    # wide range, so the LOWEST candidate (0.50) should win (max recall).
    probs = [0.99, 0.98, 0.97, 0.05, 0.04]
    y = [1, 1, 1, 0, 0]
    t, prec = select_threshold(probs, y, bar=0.85)
    assert t == 0.50, f"expected most-permissive 0.50, got {t}"
    assert prec == 1.0
    print(f"ok: chose most permissive threshold {t} when many clear")


def test_falls_back_when_bar_unreachable():
    # Heavy class overlap: no threshold reaches precision 0.99. Must return a
    # candidate (the most precise), not crash.
    probs = [0.9, 0.8, 0.7, 0.85, 0.75]
    y = [1, 0, 1, 0, 1]
    t, prec = select_threshold(probs, y, bar=0.99)
    assert 0.50 <= t <= 0.99, f"threshold {t} out of range"
    assert 0.0 <= prec <= 1.0
    print(f"ok: fell back to most-precise threshold {t} (precision {prec:.2f})")


if __name__ == "__main__":
    test_softmax_rows_sum_to_one()
    test_picks_lowest_threshold_clearing_bar()
    test_prefers_lower_threshold_when_multiple_clear()
    test_falls_back_when_bar_unreachable()
    print("all threshold self-checks passed")
