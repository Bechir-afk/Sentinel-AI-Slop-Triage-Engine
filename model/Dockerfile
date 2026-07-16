# Python inference service. CPU-only PyTorch is plenty for PR-rate load
# (see PROJECT_MAP: single 512-token forward pass, sub-second on commodity CPU).
FROM python:3.11-slim

WORKDIR /app

# Install CPU-only torch + deps first so the layer caches across code edits.
COPY requirements.txt ./
RUN pip install --no-cache-dir --extra-index-url https://download.pytorch.org/whl/cpu \
    -r requirements.txt

COPY app.py inference.py ./

# The fine-tuned artifact (weights + tokenizer) is mounted or baked at ./model.
# MODEL_PATH defaults to ./model; PORT defaults to 9000.
EXPOSE 9000

# Shell form so ${PORT} is honored (config: PORT defaults to 9000).
CMD uvicorn app:app --host 0.0.0.0 --port ${PORT:-9000}
