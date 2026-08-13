# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.6
ARG ALPINE_VERSION=3.24.1

# What `ziba version` will report. It has to be passed in
ARG VERSION=unknown

# --- Build stage -------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build

ARG VERSION

WORKDIR /src

# Dependencies first, so editing source does not invalidate the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off is what makes the binary static, and static is what lets the runtime
# image stay this small.
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/ziba ./cmd/ziba

# --- Final image -------------------------------------------------------------
FROM alpine:${ALPINE_VERSION}

ARG GO_VERSION
ARG ALPINE_VERSION
ARG VERSION

# ca-certificates: sources are read over HTTPS and would all fail without it.
# tzdata: the digest is scheduled in local time, and without this the container
#   only knows UTC — a digest set for 06:30 would arrive at the wrong hour.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=build /out/ziba /usr/local/bin/ziba

# log/ takes the model journal when ZIBA_MODEL_JOURNAL is on.
RUN mkdir -p /app/config /app/log \
 && chown nobody /app/config /app/log

VOLUME ["/app/log"]

# Nothing here needs to be root.
USER nobody

EXPOSE 8080

# wget is busybox's, already in the base image. Belongs here rather than in the
# compose file: it is a property of the image, and true however it is run.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/ >/dev/null || exit 1

ENTRYPOINT ["ziba"]
CMD ["serve"]

# The release workflow passes its own OCI labels and those win over these. These
# are for an image built by hand.
LABEL org.opencontainers.image.source="https://github.com/emaori/ziba" \
      org.opencontainers.image.description="Self-hosted personal content aggregator: collects articles, curates them with AI, serves a daily magazine" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      io.ziba.go.version="${GO_VERSION}" \
      io.ziba.alpine.version="${ALPINE_VERSION}"
