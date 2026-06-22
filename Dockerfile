FROM node:22-bookworm AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
COPY internal/httpserver/web /placeholder
RUN npm run build   # writes to ../internal/httpserver/web via vite outDir

FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/httpserver/web ./internal/httpserver/web
RUN CGO_ENABLED=0 go build -o /pr-review-dashboard .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /pr-review-dashboard /usr/local/bin/pr-review-dashboard
COPY projects.example.json /app/projects.example.json
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
WORKDIR /app
VOLUME ["/data"]
ENV HOME=/root
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD curl -f http://localhost:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
