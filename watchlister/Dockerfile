# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPROXY=direct
ENV GOSUMDB=off

RUN go mod download

COPY main.go ./

RUN go build -o configmap-watchlister main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

WORKDIR /home/appuser

COPY --from=builder /app/configmap-watchlister .

RUN chown appuser:appuser configmap-watchlister && \
    chmod +x configmap-watchlister

USER appuser

ENTRYPOINT ["./configmap-watchlister"]
