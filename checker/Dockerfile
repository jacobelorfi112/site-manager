FROM golang:1.22-alpine AS builder
WORKDIR /app
# git is required by go mod tidy to fetch dependencies from GitHub
RUN apk add --no-cache git
COPY go.mod ./
COPY *.go ./
# tidy generates go.sum and downloads all dependencies
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o checker .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/checker .
CMD ["./checker"]
