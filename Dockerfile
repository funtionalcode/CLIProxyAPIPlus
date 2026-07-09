# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN set -eux; \
    apt_no_proxy() { env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u FTP_PROXY -u http_proxy -u https_proxy -u all_proxy -u ftp_proxy "$@"; }; \
    printf '%s\n' \
        'Acquire::Retries "5";' \
        'Acquire::http::Pipeline-Depth "0";' \
        'Acquire::https::Pipeline-Depth "0";' \
        'Acquire::http::No-Cache "true";' \
        'Acquire::BrokenProxy "true";' \
        > /etc/apt/apt.conf.d/99proxy-retries; \
    apt_no_proxy apt-get update; \
    apt_no_proxy apt-get install -y --no-install-recommends build-essential git; \
    rm -rf /var/lib/apt/lists/*

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG ALL_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG all_proxy
ARG no_proxy
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org

ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    ALL_PROXY=${ALL_PROXY} \
    NO_PROXY=${NO_PROXY} \
    http_proxy=${http_proxy} \
    https_proxy=${https_proxy} \
    all_proxy=${all_proxy} \
    no_proxy=${no_proxy} \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/server/ ./cmd/server/
COPY internal/ ./internal/
COPY sdk/ ./sdk/

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm

ENV DEBIAN_FRONTEND=noninteractive

# Install base runtime dependencies.
RUN set -eux; \
    apt_no_proxy() { env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u FTP_PROXY -u http_proxy -u https_proxy -u all_proxy -u ftp_proxy "$@"; }; \
    printf '%s\n' \
        'Acquire::Retries "5";' \
        'Acquire::http::Pipeline-Depth "0";' \
        'Acquire::https::Pipeline-Depth "0";' \
        'Acquire::http::No-Cache "true";' \
        'Acquire::BrokenProxy "true";' \
        > /etc/apt/apt.conf.d/99proxy-retries; \
    apt_no_proxy apt-get update; \
    apt_no_proxy apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    python3 \
    python3-pip \
    python3-venv \
    wget \
    gnupg2 \
    libnss3 \
    libatk-bridge2.0-0 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    libxshmfence1 \
    libx11-xcb1 \
    libxcb1 \
    libxfixes3 \
    libxkbcommon0 \
    fonts-liberation \
    xdg-utils \
    telnet \
    && rm -rf /var/lib/apt/lists/*

# Install Playwright.
RUN set -eux; \
    apt_no_proxy() { env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u FTP_PROXY -u http_proxy -u https_proxy -u all_proxy -u ftp_proxy "$@"; }; \
    python3 -m venv /opt/playwright-venv; \
    /opt/playwright-venv/bin/pip install --no-cache-dir playwright; \
    /opt/playwright-venv/bin/python -m playwright install chromium; \
    apt_no_proxy /opt/playwright-venv/bin/python -m playwright install-deps chromium

ENV PATH="/opt/playwright-venv/bin:${PATH}"

# Time zone rarely changes.
ENV TZ=Asia/Shanghai
RUN ln -sf /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

# Copy the Go binary last to maximize layer cache reuse.
RUN mkdir /CLIProxyAPI
COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

VOLUME ["/root/.cli-proxy-api", "/CLIProxyAPI/plugins"]

EXPOSE 8317

CMD ["./CLIProxyAPI"]
