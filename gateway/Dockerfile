# Gateway подключает модуль Ledger через `replace => ../ledger`, поэтому сборке
# нужен именованный контекст `ledger` с исходниками соседнего модуля.
# В docker-compose.yml он задан через build.additional_contexts, вручную:
#   docker build --build-context ledger=./ledger -t gateway ./gateway
ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src/gateway
# Слой зависимостей пересобирается только при изменении go.mod/go.sum.
COPY --from=ledger go.mod go.sum /src/ledger/
COPY go.mod go.sum ./
RUN go mod download
COPY --from=ledger . /src/ledger/
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway .

FROM alpine:${ALPINE_VERSION} AS runner
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/gateway ./gateway
USER app
EXPOSE 8080
ENTRYPOINT ["./gateway"]
