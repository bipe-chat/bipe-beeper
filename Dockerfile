FROM golang:1.27-alpine AS builder
RUN apk add --no-cache git build-base
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN ./build.sh

FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec
COPY --from=builder /build/bipe-beeper /usr/bin/bipe-beeper
VOLUME /data
WORKDIR /data
ENTRYPOINT ["/usr/bin/bipe-beeper", "-c", "/data/config.yaml"]
