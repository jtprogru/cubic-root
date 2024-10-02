FROM golang:1.23-alpine3.20 AS builder
LABEL authors="Mikhail Savin <jtprogru@gmail.com>"

ARG http_port=8080
ARG debug_mode=false

ENV PORT=$http_port
ENV DEBUG=$debug_mode

WORKDIR /go/src/github.com/jtprogru/cubic-root

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /go/bin/cubic-root

# Используем легковесный alpine образ для финального контейнера
FROM alpine:3.20
LABEL authors="Mikhail Savin <jtprogru@gmail.com>"

# Копируем скрипт запуска
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

# Копируем собранное приложение
COPY --from=builder /go/bin/cubic-root /cubic-root

# Устанавливаем переменные окружения
ENV PORT=$PORT
ENV DEBUG=$DEBUG

EXPOSE $PORT

ENTRYPOINT ["/docker-entrypoint.sh"]

CMD ["/cubic-root"]
