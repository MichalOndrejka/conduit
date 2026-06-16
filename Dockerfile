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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /conduit /conduit

VOLUME /data
ENV CONDUIT_DATA_DIR=/data
EXPOSE 8000
ENTRYPOINT ["/conduit"]
