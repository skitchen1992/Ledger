# Отдельный HTTP-сервис Ledger (порт 8081). В docker-compose.yml пока не запускается:
#   docker build -t ledger ./ledger
ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
# Слой зависимостей пересобирается только при изменении go.mod/go.sum.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger ./cmd/ledger

FROM alpine:${ALPINE_VERSION} AS runner
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/ledger ./ledger
USER app
EXPOSE 8081
ENTRYPOINT ["./ledger"]
