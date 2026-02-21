# Stage 1: Build Vue.js frontend
FROM node:22-alpine AS frontend
RUN corepack enable && corepack prepare pnpm@10.28.2 --activate
WORKDIR /app/web/frontend
COPY web/frontend/package.json web/frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/frontend/ ./
RUN pnpm run build

# Stage 2: Build Go binary
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo docker)" -o murmur ./cmd/murmur

# Stage 3: Runtime
FROM alpine:3.21
RUN apk --no-cache add ca-certificates docker-cli git whois bind-tools
COPY --from=builder /app/murmur /usr/local/bin/murmur
ENTRYPOINT ["murmur"]
