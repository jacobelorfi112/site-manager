# Go site-manager service: dashboard, /check API, and background site-check worker.
# Requires Chromium (go-rod drives it headless for checkout validation).
FROM golang:1.25-bookworm AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o site-manager .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    chromium ca-certificates fonts-liberation \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /app/site-manager .
EXPOSE 8080
CMD ["./site-manager"]
