FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zpika-feedback .

FROM alpine:3.23

RUN addgroup -S -g 10001 feedback && \
    adduser -S -D -H -u 10001 -G feedback feedback && \
    apk add --no-cache ca-certificates tzdata && \
    mkdir -p /data && \
    chown feedback:feedback /data
COPY --from=build /out/zpika-feedback /usr/local/bin/zpika-feedback
USER feedback
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/zpika-feedback"]
