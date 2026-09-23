# Python inference service. CPU-only PyTorch is plenty for PR-rate load
# (see PROJECT_MAP: single 512-token forward pass, sub-second on commodity CPU).
FROM python:3.11-slim

WORKDIR /app

# Install CPU-only torch in a DEDICATED step from the PyTorch CPU index, so the
# heavy CUDA build is never pulled (smaller image, no GPU assumptions — R8).
# Keep the pin in sync with requirements.txt.
RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.5.1

# Then the rest of the deps from PyPI (torch is already satisfied above).
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py inference.py encoding.py ./

# Non-root user (FR-013/FR-014). HF_HOME points at /tmp so transformers has a
# writable cache when the container runs with a read-only rootfs + tmpfs /tmp
# (compose T018). The artifact itself is mounted read-only at MODEL_PATH.
ENV HF_HOME=/tmp/hf
RUN useradd --create-home --uid 10001 appuser
USER appuser

# The fine-tuned artifact (weights + tokenizer) is mounted or baked at ./model.
# MODEL_PATH defaults to ./model; PORT defaults to 9000.
EXPOSE 9000

# Shell form so ${PORT} is honored (config: PORT defaults to 9000).
CMD uvicorn app:app --host 0.0.0.0 --port ${PORT:-9000}
