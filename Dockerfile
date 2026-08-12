# Ziba — a static binary in a small image.
#
# Two stages: build with the toolchain, ship without it. The result carries the
# binary, the certificates it needs to reach sources over HTTPS, and the
# timezone database — nothing else.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so editing source does not invalidate the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# What `ziba version` will report. It has to be passed in — .dockerignore keeps
# .git out of the build context, deliberately, so the build cannot work it out
# for itself. The Makefile and the release workflow both supply it.
#
# The fallback says "unknown" rather than naming a build system, because that is
# what it means: nobody told this build what it was. It was "docker", which read
# like a version and was not one.
ARG VERSION=unknown
# CGO off is what makes the binary static, and static is what lets the runtime
# image stay this small.
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/ziba ./cmd/ziba

FROM alpine:3.20

# ca-certificates: sources are read over HTTPS and would all fail without it.
# tzdata: the digest is scheduled in local time, and without this the container
#   only knows UTC — a digest set for 06:30 would arrive at the wrong hour.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=build /out/ziba /usr/local/bin/ziba

# The configuration directory, empty.
#
# No starter is baked in, deliberately. An image that boots with somebody else's
# interests looks like it is working while collecting things nobody asked for,
# and the reader has no reason to look at the log. Empty means a new instance
# stops on its first run and says exactly what is missing and where to put it.
#
# Mount a directory holding interests.yaml and sources.yaml here.
RUN mkdir -p /app/config && chown nobody /app/config

# Where the model journal is written when ZIBA_MODEL_JOURNAL is on. Created
# here, and owned by the user the process runs as, because that process cannot
# create it: /app belongs to root and nobody may not write there. Bind-mount it
# to read the file from outside.
RUN mkdir -p /app/log && chown nobody /app/log
VOLUME ["/app/log"]

# Nothing here needs to be root.
USER nobody

EXPOSE 8080

ENTRYPOINT ["ziba"]
CMD ["serve"]
