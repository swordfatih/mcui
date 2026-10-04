# syntax=docker/dockerfile:1

FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /mcui ./cmd/mcui
RUN CGO_ENABLED=0 go build -o /dimension-worker ./cmd/dimension-worker

FROM docker:28-cli AS docker-cli
FROM debian:bookworm-slim AS runner
RUN apt-get update && apt-get install -y --no-install-recommends rclone ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
COPY --from=backend /dimension-worker /app/dimension-worker
WORKDIR /app
COPY --from=backend /mcui /app/mcui
COPY --from=web /src/web/dist /app/web/dist
ENV MCUI_DIMENSION_WORKER=/app/dimension-worker
ENV MCUI_ADDR=0.0.0.0:8080 MCUI_SERVERS_DIR=/servers
EXPOSE 8080
CMD ["/app/mcui"]
