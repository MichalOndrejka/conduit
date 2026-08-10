# Conduit (Go) — multi-stage build producing a ~25 MB distroless image
# (vs the ~1–2 GB Python image), idling at ~20–50 MB RSS.
# Named Dockerfile.golang (not .go) so the Go toolchain doesn't try to compile it.
FROM golang:1.26 AS build
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
# docker-compose bind-mounts config.json there, and Docker would otherwise
# auto-create that directory as root:root, blocking config.json.tmp writes.
RUN mkdir -p /data-empty /app-empty && chown 65532:65532 /data-empty /app-empty

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /conduit /conduit
COPY --from=build --chown=65532:65532 /data-empty /data
COPY --from=build --chown=65532:65532 /app-empty /app

VOLUME /data
ENV CONDUIT_DATA_DIR=/data
EXPOSE 8000
ENTRYPOINT ["/conduit"]
