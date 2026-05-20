# Build stage
FROM golang:1.25-alpine AS builder

# Install git (required for GOPROXY=direct)
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Set environment variables
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOPROXY=direct
ENV GOSUMDB=off

# Download dependencies (cached unless go.mod/go.sum change)
RUN go mod download

# Copy source code
COPY main.go ./

# Build the binary
RUN go build -o pod-watcher main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

# Create user first
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Set working directory to appuser's home
WORKDIR /home/appuser

# Copy the binary from builder
COPY --from=builder /app/pod-watcher .

# Set ownership and permissions
RUN chown appuser:appuser pod-watcher && \
    chmod +x pod-watcher

USER appuser

ENTRYPOINT ["./pod-watcher"]
