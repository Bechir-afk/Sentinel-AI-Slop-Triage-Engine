# --- build stage ---
# Pinned by digest (FR-012): the tag names the version for humans, the @sha256
# makes the pull reproducible and tamper-evident. Refresh both together when
# bumping Go (resolve with `docker buildx imagetools inspect golang:<tag>`).
FROM golang:1.26@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS build
WORKDIR /src

# No third-party deps, so no module cache to prime — COPY . . brings in go.mod
# and all sources in one step.
COPY . .

# Static, stripped binary — no libc, no debug info.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /sentinel ./cmd/sentinel

# --- runtime stage ---
# distroless static ships CA certs (needed for HTTPS to GitHub) and
# runs as non-root. Final image is a few MB, well under the 20MB target.
# Digest-pinned (FR-012) alongside the :nonroot tag.
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /sentinel /sentinel
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/sentinel"]
