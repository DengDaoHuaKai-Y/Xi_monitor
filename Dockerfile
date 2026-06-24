# syntax=docker/dockerfile:1

FROM node:22-alpine AS frontend-builder
WORKDIR /src/frontend

COPY frontend/package*.json ./
RUN npm ci

COPY frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend-builder
WORKDIR /src/backend

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/xi-monitor ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S app \
	&& adduser -S -G app app

WORKDIR /app
COPY --from=backend-builder /out/xi-monitor /app/xi-monitor
COPY --from=frontend-builder /src/frontend/dist /app/frontend/dist

ENV GIN_MODE=release \
	MID_SERVER_ADDR=:8080 \
	MID_FRONTEND_DIST=/app/frontend/dist

EXPOSE 8080
USER app
ENTRYPOINT ["/app/xi-monitor"]
CMD ["-config", "/app/config/config.yaml"]
