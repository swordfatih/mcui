# syntax=docker/dockerfile:1

FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /mcui ./cmd/mcui

FROM alpine:3.22 AS runner
RUN apk add --no-cache docker-cli docker-cli-compose restic rclone ca-certificates
WORKDIR /app
COPY --from=backend /mcui /app/mcui
COPY --from=web /src/web/dist /app/web/dist
ENV MCUI_ADDR=0.0.0.0:8080 MCUI_SERVERS_DIR=/servers
EXPOSE 8080
CMD ["/app/mcui"]
