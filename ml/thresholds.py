"""Pure-numpy threshold selection, split out from train.py so it carries no
torch/transformers import and can be unit-checked offline (T014, FR-010).

The model service and evaluate.py operate at a single confidence cutoff; this
picks that cutoff on the validation split.
"""

import logging

import numpy as np

logger = logging.getLogger("sentinel.train")

SLOP = 1


def softmax(logits: np.ndarray) -> np.ndarray:
    """Row-wise softmax, numerically stabilized."""
    z = logits - logits.max(axis=-1, keepdims=True)
    e = np.exp(z)
    return e / e.sum(axis=-1, keepdims=True)


def select_threshold(slop_probs, y_true, bar: float):
    """Pick the LOWEST threshold whose SLOP precision on validation clears `bar`.

    Lower threshold = more PRs flagged = higher recall; we take the most
    permissive cutoff that still keeps precision at/above the bar, so we flag as
    much slop as we can without dropping below the precision floor that protects
    real contributors (constitution III). Returns (threshold, precision). If no
    candidate clears the bar, returns the most precise candidate with a warning
    so the caller still gates honestly.
    """
    slop_probs = np.asarray(slop_probs, dtype=float)
    y_true = np.asarray(y_true)
    candidates = [round(float(t), 2) for t in np.arange(0.50, 0.991, 0.01)]

    best_clearing = None  # (threshold, precision) — lowest threshold that clears
    most_precise = None   # fallback: (precision, threshold)
    for t in candidates:
        pred = (slop_probs >= t).astype(int)
        tp = int(((pred == SLOP) & (y_true == SLOP)).sum())
        fp = int(((pred == SLOP) & (y_true != SLOP)).sum())
        if tp + fp == 0:
            continue  # nothing flagged at this cutoff — undefined precision
        prec = tp / (tp + fp)
        if most_precise is None or prec > most_precise[0]:
            most_precise = (prec, t)
        if prec >= bar and best_clearing is None:
            best_clearing = (t, prec)

    if best_clearing is not None:
        return best_clearing
    logger.warning("no threshold cleared precision bar %.2f on validation; "
                   "using most-precise candidate", bar)
    if most_precise is None:
        return 0.99, 0.0  # degenerate: nothing ever flagged
    return most_precise[1], most_precise[0]
