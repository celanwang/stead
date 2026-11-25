# syntax=docker/dockerfile:1
FROM golang:1.27.3-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stead ./cmd/stead

FROM alpine:3.24

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 stead \
    && adduser -S -D -H -u 10001 -G stead stead \
    && mkdir -p /var/lib/stead/deliveries \
    && chown -R 10001:10001 /var/lib/stead \
    && chmod 0700 /var/lib/stead/deliveries

COPY --from=build /out/stead /usr/local/bin/stead
WORKDIR /var/lib/stead
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/stead"]
CMD ["--help"]
