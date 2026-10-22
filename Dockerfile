# Conduit (Go) — multi-stage build producing a ~25 MB distroless image
# (vs the ~1–2 GB Python image), idling at ~20–50 MB RSS.
FROM golang:1.27.3 AS build
WORKDIR /src

# Cache module downloads separately from source changes
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /conduit ./cmd/conduit

# Distroless "nonroot" runs as UID 65532; pre-own /data here (root, in this
# build stage) so Docker seeds a freshly created named volume with the right
# owner instead of root:root, which the app can't write to. Same for /app:
# a bind-mounted config.json commonly lives there, and Docker would otherwise
# auto-create that directory as root:root, blocking config.json.tmp writes.
RUN mkdir -p /data-empty /app-empty && chown 65532:65532 /data-empty /app-empty

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /conduit /conduit
COPY --from=build --chown=65532:65532 /data-empty /data
COPY --from=build --chown=65532:65532 /app-empty /app

VOLUME /data
ENV CONDUIT_DATA_DIR=/data
# First-run Settings defaults point at host.docker.internal instead of localhost
# (and the web server binds 0.0.0.0 instead of loopback, so -p works)
ENV CONDUIT_IN_CONTAINER=1
EXPOSE 8000
# No shell/curl in distroless — the binary probes its own /health endpoint
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/conduit", "healthcheck"]
ENTRYPOINT ["/conduit"]
