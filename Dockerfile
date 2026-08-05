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

ARG VERSION=docker
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

# A working default. Compose mounts the real one over it, so the hand-edited
# files can be changed without rebuilding the image.
COPY config /app/config

# Nothing here needs to be root.
USER nobody

EXPOSE 8080

ENTRYPOINT ["ziba"]
CMD ["serve"]
