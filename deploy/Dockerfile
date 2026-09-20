# --- build stage ---
FROM golang:1.26 AS build
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
FROM gcr.io/distroless/static:nonroot
COPY --from=build /sentinel /sentinel
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/sentinel"]
