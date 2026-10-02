# Go checker service: CF Worker API mode (no PostgreSQL needed).
# Connects to the CF Worker for site data, dashboard lives on CF.
FROM golang:1.25-bookworm AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -tags cfworker -o site-manager .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /app/site-manager .
EXPOSE 8080
CMD ["./site-manager"]
