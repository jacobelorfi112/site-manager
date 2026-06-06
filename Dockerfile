FROM golang:1.25-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -tags '!ignore' -o /checker .

FROM debian:bookworm-slim

# Install Chromium and required shared libraries
RUN apt-get update && apt-get install -y --no-install-recommends \
    chromium \
    fonts-liberation \
    fonts-noto-color-emoji \
    libnss3 \
    libatk-bridge2.0-0 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libgbm1 \
    libasound2 \
    libpango-1.0-0 \
    libcairo2 \
    libcups2 \
    libdbus-1-3 \
    libexpat1 \
    libfontconfig1 \
    libgcc-s1 \
    libglib2.0-0 \
    libgtk-3-0 \
    libnspr4 \
    libx11-6 \
    libx11-xcb1 \
    libxcb1 \
    libxext6 \
    libxfixes3 \
    libxss1 \
    libxtst6 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Rod launcher detects Chromium at this path
ENV CHROME_PATH=/usr/bin/chromium

# Create non-root user
RUN useradd -m -s /bin/bash checker
USER checker

COPY --from=builder /checker /usr/local/bin/checker

EXPOSE 8080

CMD ["sh", "-c", "ulimit -u 65535 2>/dev/null; ulimit -n 65535 2>/dev/null; exec checker"]
