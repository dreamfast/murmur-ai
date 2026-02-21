FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo docker)" -o murmur ./cmd/murmur

FROM alpine:latest
RUN apk --no-cache add ca-certificates docker-cli git whois bind-tools
COPY --from=builder /app/murmur /usr/local/bin/murmur
ENTRYPOINT ["murmur"]
