ARG BUILDER_IMAGE=mcr.microsoft.com/devcontainers/go:2.1.6-1.26-bookworm
ARG RUNTIME_IMAGE=mcr.microsoft.com/mirror/docker/library/debian:bookworm-slim
FROM ${BUILDER_IMAGE} AS builder

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        autoconf \
        bison \
        build-essential \
        ca-certificates \
        curl \
        file \
        libcurl4-openssl-dev \
        libfreetype6-dev \
        libicu-dev \
        libjpeg62-turbo-dev \
        libldap2-dev \
        libonig-dev \
        libpng-dev \
        libpq-dev \
        libsasl2-dev \
        libsqlite3-dev \
        libssl-dev \
        libxml2-dev \
        libzip-dev \
        patch \
        pkg-config \
        re2c \
        xz-utils \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION
ARG PHP_VERSION
ARG PHP_URL
ARG PHP_SHA256
ARG FRANKENPHP_VERSION
ARG CADDY_VERSION
ARG IONCUBE_VERSION
ARG IONCUBE_URL
ARG IONCUBE_ARCHIVE_SHA256
ARG IONCUBE_LOADER
ARG IONCUBE_LOADER_SHA256
ARG LOADER_ARCH=amd64
ARG PHP_LIBDIR=lib/x86_64-linux-gnu

RUN test "$(go version | awk '{sub(/^go/, "", $3); print $3}')" = "${GO_VERSION}"

WORKDIR /build
COPY patches/php/8.4/0001-ioncube-zend-signal-abi-shim.patch /build/php-ioncube.patch
COPY packaging/poc/expected-ioncube-symbols.txt /build/expected-ioncube-symbols.txt
COPY packaging/poc/expected-ioncube-symbols-${LOADER_ARCH}.txt /build/expected-ioncube-symbols-loader.txt

RUN curl --fail --location --retry 3 --output php.tar.xz "${PHP_URL}" \
    && echo "${PHP_SHA256}  php.tar.xz" | sha256sum --check --strict \
    && tar -xJf php.tar.xz \
    && cd "php-${PHP_VERSION}" \
    && patch --forward --fuzz=0 -p1 < /build/php-ioncube.patch \
    && ./configure \
        --prefix=/opt/zentao/php \
        --with-config-file-path=/opt/zentao/config \
        --with-config-file-scan-dir=/opt/zentao/config/conf.d \
        --with-libdir=${PHP_LIBDIR} \
        --enable-embed=shared \
        --enable-zts \
        --disable-zend-signals \
        --disable-cgi \
        --enable-bcmath \
        --enable-exif \
        --enable-gd \
        --enable-intl \
        --enable-mbstring \
        --enable-opcache \
        --enable-sockets \
        --with-curl \
        --with-freetype \
        --with-jpeg \
        --with-ldap \
        --with-mysqli=mysqlnd \
        --with-openssl \
        --with-pdo-mysql=mysqlnd \
        --with-pdo-pgsql \
        --with-pgsql \
        --with-zip \
        --with-zlib \
    && make -j"$(nproc)" \
    && make install

RUN mkdir -p /opt/zentao/php/lib/ioncube \
    && curl --fail --location --retry 3 --output ioncube.tar.gz "${IONCUBE_URL}" \
    && echo "${IONCUBE_ARCHIVE_SHA256}  ioncube.tar.gz" | sha256sum --check --strict \
    && tar -xzf ioncube.tar.gz \
    && echo "${IONCUBE_LOADER_SHA256}  ioncube/${IONCUBE_LOADER}" | sha256sum --check --strict \
    && cp "ioncube/${IONCUBE_LOADER}" /opt/zentao/php/lib/ioncube/ \
    && readelf -Ws "/opt/zentao/php/lib/ioncube/${IONCUBE_LOADER}" \
        | awk '$7 == "UND" && $8 ~ /^zend_signal/ {print $8}' \
        | sort -u \
        > /tmp/ioncube-loader-symbols.txt \
    && sed '/^$/d' /build/expected-ioncube-symbols-loader.txt > /tmp/expected-loader-symbols.txt \
    && diff -u /tmp/expected-loader-symbols.txt /tmp/ioncube-loader-symbols.txt

COPY packaging/poc/php.ini /opt/zentao/config/php.ini
RUN mkdir -p /opt/zentao/config/conf.d /opt/zentao/bin \
    && test "$(/opt/zentao/php/bin/php-config --vernum)" -ge 80400 \
    && nm -D /opt/zentao/php/lib/libphp.so \
        | awk '$3 ~ /^zend_signal_(globals_id|globals_offset|handler_unblock)$/ {print $3}' \
        | sort -u \
        | diff -u /build/expected-ioncube-symbols.txt -

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN go test ./internal/config ./internal/lifecycle ./internal/health ./internal/control \
    ./internal/logging ./internal/upgrade ./internal/queue/bridge ./internal/queue/fake \
    ./internal/queue/client ./internal/queue/lease ./internal/queue/worker ./internal/queue/engine ./internal/scheduler \
    ./internal/observability ./internal/observability/query ./internal/observability/retention \
    ./internal/observability/jsonl \
    ./internal/observability/duckdb

RUN go test -tags duckdb ./internal/observability/duckdb

RUN export CGO_CFLAGS="$(/opt/zentao/php/bin/php-config --includes)" \
    && export CGO_LDFLAGS="-L/opt/zentao/php/lib -Wl,-rpath,/opt/zentao/php/lib -lphp $(/opt/zentao/php/bin/php-config --ldflags) $(/opt/zentao/php/bin/php-config --libs)" \
    && go build \
        -tags "duckdb nobadger nomysql nopgx nowatcher nobrotli nomercure" \
        -ldflags "-s -w -X main.runtimeVersion=poc -X main.frankenPHPVersion=${FRANKENPHP_VERSION} -X main.caddyVersion=${CADDY_VERSION}" \
        -o /opt/zentao/bin/zentao-runtime \
        ./cmd/zentao-runtime

RUN find \
        /opt/zentao/php/include \
        /opt/zentao/php/lib/php/build \
        /opt/zentao/php/php \
        -depth -delete \
    && unlink /opt/zentao/php/bin/phpdbg \
    && unlink /opt/zentao/php/bin/phpize \
    && unlink /opt/zentao/php/bin/php-config

FROM ${RUNTIME_IMAGE} AS runtime
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
RUN sed -i \
        -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
        -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
        /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        libcurl4 \
        libfreetype6 \
        libicu72 \
        libjpeg62-turbo \
        libldap-2.5-0 \
        libonig5 \
        libpng16-16 \
        libpq5 \
        libsasl2-2 \
        libsqlite3-0 \
        libssl3 \
        libxml2 \
        libzip4 \
        zlib1g \
    && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /opt/zentao/logs
COPY --from=builder /opt/zentao /opt/zentao
COPY --chmod=0755 packaging/docker/healthcheck.sh /opt/zentao/packaging/docker/healthcheck.sh
ENV PATH="/opt/zentao/bin:/opt/zentao/php/bin:${PATH}"
ENTRYPOINT ["/opt/zentao/bin/zentao-runtime"]

FROM scratch AS artifact
COPY --from=builder /opt/zentao /
