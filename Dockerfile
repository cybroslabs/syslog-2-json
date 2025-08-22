# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24.6-alpine AS builder
ARG TARGETOS TARGETARCH

RUN apk --no-cache add ca-certificates \
    && update-ca-certificates

WORKDIR /build

COPY go.mod go.sum ./

RUN go mod download

COPY ./internal ./internal
COPY ./cmd/syslog-2-json ./cmd/syslog-2-json

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -a -o syslog-2-json ./cmd/syslog-2-json

FROM gcr.io/distroless/static-debian12:nonroot AS syslog-2-json

WORKDIR /app

COPY --from=builder /build/syslog-2-json .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

CMD [ "/app/syslog-2-json" ]
