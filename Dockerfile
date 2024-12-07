FROM golang:1.23.4-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./

RUN go mod download

COPY ./internal ./internal
COPY ./cmd/syslog-2-json ./cmd/syslog-2-json

RUN go build -a -o syslog-2-json ./cmd/syslog-2-json

FROM alpine:3.21 AS syslog-2-json

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /build/syslog-2-json .

CMD [ "/app/syslog-2-json" ]
