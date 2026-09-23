"""Shared title+diff pair-encoding — the ONE place the model service builds its
tokenizer input (FR-008).

CodeBERT is a sentence-pair model. The old input built one string,
"<title>\\n[SEP]\\n<diff>", which makes "[SEP]" a literal 5-token substring (not
the real separator) and head-truncates the whole thing blindly — so a long title
could evict the entire diff. The tokenizer's pair API inserts the model's real
separator (</s></s> for RoBERTa/CodeBERT) and, with truncation="only_second",
truncates the DIFF while keeping the title. We first cap the title's own token
budget so a pathological long title still can't starve the diff of window.

ml/encoding.py is a byte-for-byte twin of this file (the model service and the
offline ml/ trainer are separate build contexts with no shared import path).
The two MUST stay identical or train/serve encoding skews silently.
"""

MAX_TOKENS = 512
# PR titles are short (GitHub caps them at 256 chars); reserve the rest of the
# 512-token window for the diff so the diff always gets scored.
TITLE_MAX_TOKENS = 64


def _cap_title(tokenizer, title: str) -> str:
    """Truncate title to at most TITLE_MAX_TOKENS tokens, returned as text so the
    pair call re-inserts the real special tokens around it."""
    ids = tokenizer.encode(
        title, add_special_tokens=False, truncation=True, max_length=TITLE_MAX_TOKENS
    )
    return tokenizer.decode(ids)


def encode_pair(tokenizer, title, diff, **kwargs):
    """Encode (title, diff) as a CodeBERT sentence pair with the real separator,
    truncating only the diff. title/diff may be single strings or equal-length
    lists (batched, for training). Extra kwargs (padding, return_tensors) pass
    through to the tokenizer."""
    if isinstance(title, (list, tuple)):
        title = [_cap_title(tokenizer, t) for t in title]
    else:
        title = _cap_title(tokenizer, title)
    return tokenizer(title, diff, truncation="only_second", max_length=MAX_TOKENS, **kwargs)
