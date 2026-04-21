FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN set -eux; \
    mkdir -p /out; \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
      -o /out/CLIProxyAPI ./cmd/server/; \
    ls -la /out/CLIProxyAPI; \
    test -s /out/CLIProxyAPI; \
    echo "BUILD_OK size=$(stat -c %s /out/CLIProxyAPI)"

FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates

RUN mkdir -p /CLIProxyAPI

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
RUN set -eux; \
    chmod +x /CLIProxyAPI/CLIProxyAPI; \
    ls -la /CLIProxyAPI/CLIProxyAPI; \
    test -s /CLIProxyAPI/CLIProxyAPI

COPY config.example.toml /CLIProxyAPI/config.example.toml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["sh", "-c", "[ -f config.toml ] || cp config.example.toml config.toml; exec ./CLIProxyAPI"]