"""Self-check for the leakage invariant in build_dataset (T013, FR-009).

Pure stdlib — no pandas/torch — so it runs in CI and locally without the ML
deps. Verifies: (1) a real build produces zero cross-split leakage, (2) the
assertion actually fires when an identical row is planted in two splits.

Run: python ml/tests/test_build_dataset.py   (exit 0 = pass)
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import build_dataset as bd  # noqa: E402


def test_clean_build_has_no_leakage():
    # 40 real rows (balanced) + 300 synthetic, split and checked.
    raw = [{"title": f"real pr {i}", "diff": f"diff body {i}", "label": i % 2} for i in range(40)]
    splits = bd.build(raw, synthetic_total=300, seed=42)  # raises on leak
    assert sum(len(v) for v in splits.values()) == 40 + 300, "row count mismatch"
    # Every split is non-empty given this budget.
    for name, rows in splits.items():
        assert rows, f"{name} split is empty"
    print("ok: clean build, no cross-split leakage")


def test_planted_collision_is_caught():
    dup = {"title": "dup", "diff": "same body", "label": bd.SLOP}
    splits = {"train": [dup], "val": [dup], "test": []}
    try:
        bd.assert_no_leakage(splits)
    except ValueError as e:
        assert "leakage" in str(e)
        print("ok: planted cross-split collision caught")
        return
    raise AssertionError("assert_no_leakage did not catch a planted collision")


def test_within_split_duplicate_is_allowed():
    dup = {"title": "dup", "diff": "same body", "label": bd.SLOP}
    # Same row twice in ONE split is not a leak.
    bd.assert_no_leakage({"train": [dup, dup], "val": [], "test": []})
    print("ok: within-split duplicate allowed")


def test_reproducible_with_same_seed():
    raw = [{"title": f"r{i}", "diff": f"d{i}", "label": i % 2} for i in range(20)]
    a = bd.build(raw, 100, seed=7)
    b = bd.build(raw, 100, seed=7)
    ha = {name: sorted(bd._row_hash(r) for r in rows) for name, rows in a.items()}
    hb = {name: sorted(bd._row_hash(r) for r in rows) for name, rows in b.items()}
    assert ha == hb, "same seed produced different splits"
    print("ok: reproducible with same seed")


if __name__ == "__main__":
    test_clean_build_has_no_leakage()
    test_planted_collision_is_caught()
    test_within_split_duplicate_is_allowed()
    test_reproducible_with_same_seed()
    print("all build_dataset self-checks passed")
