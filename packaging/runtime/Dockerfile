ARG BUILDER_IMAGE=mcr.microsoft.com/devcontainers/go:2.1.6-1.26-bookworm
ARG RUNTIME_IMAGE=mcr.microsoft.com/mirror/docker/library/debian:bookworm-slim
FROM ${BUILDER_IMAGE} AS builder
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security

# Apply the configured Debian mirrors before the first apt step: the base
# image points at deb.debian.org, which is not reliably reachable from every
# build network and made the PHP build hang in package downloads.
RUN set -eux; \
    for list in /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources; do \
        [ -f "${list}" ] || continue; \
        sed -i \
            -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
            -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
            "${list}"; \
    done

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
        patchelf \
        patch \
        pkg-config \
        re2c \
        xz-utils \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION
ARG PHP_VERSION
ARG PHP_API
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
COPY packaging/runtime/third-party/dm8 /build/dm8

RUN curl --fail --location --retry 3 --output php.tar.xz "${PHP_URL}" \
    && echo "${PHP_SHA256}  php.tar.xz" | sha256sum --check --strict \
    && tar -xJf php.tar.xz \
    && cd "php-${PHP_VERSION}" \
    && if [ "${LOADER_ARCH}" = "amd64" ]; then patch --forward --fuzz=0 -p1 < /build/php-ioncube.patch; fi \
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
    && if [ "${LOADER_ARCH}" = "amd64" ]; then \
        printf '%s\n' zend_signal_globals_id zend_signal_globals_offset zend_signal_handler_unblock \
            | diff -u - /tmp/ioncube-loader-symbols.txt; \
    else \
        test ! -s /tmp/ioncube-loader-symbols.txt; \
    fi

ARG PHPREDIS_COMMIT
ARG PHPREDIS_ARCHIVE_SHA256
RUN echo "building phpredis ${PHPREDIS_COMMIT}" \
    && curl --http1.1 --fail --location --retry 5 --retry-delay 2 --retry-all-errors --output /build/phpredis.tar.gz \
        "https://github.com/phpredis/phpredis/archive/${PHPREDIS_COMMIT}.tar.gz" \
    && echo "${PHPREDIS_ARCHIVE_SHA256}  /build/phpredis.tar.gz" | sha256sum --check --strict \
    && mkdir -p /build/phpredis \
    && tar -xzf /build/phpredis.tar.gz -C /build/phpredis --strip-components=1 \
    && cd /build/phpredis \
    && /opt/zentao/php/bin/phpize \
    && ./configure --with-php-config=/opt/zentao/php/bin/php-config \
    && make -j"$(nproc)" \
    && make install \
    && rm -rf /build/phpredis /build/phpredis.tar.gz

ARG APCU_COMMIT
ARG APCU_ARCHIVE_SHA256
RUN echo "building apcu ${APCU_COMMIT}" \
    && curl --http1.1 --fail --location --retry 5 --retry-delay 2 --retry-all-errors --output /build/apcu.tar.gz \
        "https://github.com/krakjoe/apcu/archive/${APCU_COMMIT}.tar.gz" \
    && echo "${APCU_ARCHIVE_SHA256}  /build/apcu.tar.gz" | sha256sum --check --strict \
    && mkdir -p /build/apcu \
    && tar -xzf /build/apcu.tar.gz -C /build/apcu --strip-components=1 \
    && cd /build/apcu \
    && /opt/zentao/php/bin/phpize \
    && ./configure --with-php-config=/opt/zentao/php/bin/php-config \
    && make -j"$(nproc)" \
    && make install \
    && rm -rf /build/apcu /build/apcu.tar.gz

RUN cd /build/dm8 \
    && sha256sum --check --strict SHA256SUMS \
    && strings "php-8.4-zts/linux-${LOADER_ARCH}/php84ts_pdo_dm.so" | grep -q 'API20240924,TS' \
    && mkdir -p /opt/zentao/php/lib/php/extensions/no-debug-zts-20240924 /opt/zentao/php/lib/dependencies \
    && cp "php-8.4-zts/linux-${LOADER_ARCH}/php84ts_pdo_dm.so" /opt/zentao/php/lib/php/extensions/no-debug-zts-20240924/pdo_dm.so \
    && cp "dpi/linux-${LOADER_ARCH}/libdmdpi.so" "dpi/linux-${LOADER_ARCH}"/components/*.so /opt/zentao/php/lib/ \
    && cp "dpi/linux-${LOADER_ARCH}"/dependencies/libcrypto.so "dpi/linux-${LOADER_ARCH}"/dependencies/libssl.so /opt/zentao/php/lib/dependencies/

COPY packaging/runtime/php.ini /opt/zentao/config/php.ini
COPY packaging/runtime/dm_svc.conf /opt/zentao/config/dm_svc.conf
COPY config/runtime.example.toml /opt/zentao/config/runtime.example.toml
COPY php /opt/zentao/lib/php
RUN mkdir -p /opt/zentao/config/conf.d /opt/zentao/bin \
    && /opt/zentao/php/bin/php -n -l /opt/zentao/lib/php/runtime-process.php \
    && export LD_LIBRARY_PATH=/opt/zentao/php/lib \
    && test "$(/opt/zentao/php/bin/php-config --vernum)" -ge 80400 \
    && sed 's#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g' /opt/zentao/config/php.ini > /tmp/php.ini \
    && if ! /opt/zentao/php/bin/php -c /tmp/php.ini -r 'exit(extension_loaded("Zend OPcache") && ini_get("opcache.enable") == "1" ? 0 : 1);'; then \
        /opt/zentao/php/bin/php -c /tmp/php.ini -v; \
        /opt/zentao/php/bin/php -c /tmp/php.ini -m; \
        /opt/zentao/php/bin/php -c /tmp/php.ini -i | grep -E '^(opcache|Zend OPcache)'; \
        exit 1; \
    fi

RUN export LD_LIBRARY_PATH=/opt/zentao/php/lib \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'exit(extension_loaded("redis") ? 0 : 1);'

RUN export LD_LIBRARY_PATH=/opt/zentao/php/lib \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'exit(extension_loaded("apcu") && ini_get("apc.enabled") == "1" ? 0 : 1);'

RUN export LD_LIBRARY_PATH=/opt/zentao/php/lib \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'exit(extension_loaded("pdo_dm") && in_array("dm", PDO::getAvailableDrivers(), true) ? 0 : 1);' \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'try { new PDO("dm:host=127.0.0.1;port=5236", "SYSDBA", "x", [PDO::ATTR_TIMEOUT=>2]); exit(2); } catch (Throwable $e) { $m = $e->getMessage(); exit((strpos($m, "-70089") !== false || stripos($m, "is missing") !== false || stripos($m, "Unable to load") !== false) ? 3 : 0); }'

RUN nm -D /opt/zentao/php/lib/libphp.so \
        | awk '$3 ~ /^zend_signal_(globals_id|globals_offset|handler_unblock)$/ {print $3}' \
        | sort -u \
        > /tmp/libphp-signal-symbols.txt \
    && if [ "${LOADER_ARCH}" = "amd64" ]; then \
        printf '%s\n' zend_signal_globals_id zend_signal_globals_offset zend_signal_handler_unblock \
            | diff -u - /tmp/libphp-signal-symbols.txt; \
    else \
        test ! -s /tmp/libphp-signal-symbols.txt; \
    fi

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY config ./config
COPY php ./php
COPY tests/fixtures/runtime/runtime.toml ./tests/fixtures/runtime/runtime.toml
COPY scripts/package/bundle-runtime-libs.sh /build/bundle-runtime-libs.sh

RUN go test ./internal/config ./internal/lifecycle ./internal/health ./internal/control \
    ./internal/appprocess \
    ./internal/logging ./internal/queue/bridge ./internal/queue/fake \
    ./internal/queue/client ./internal/queue/lease ./internal/queue/worker ./internal/queue/engine ./internal/scheduler \
    ./internal/olap \
    ./internal/cluster \
    ./internal/observability ./internal/observability/query ./internal/observability/retention \
    ./internal/observability/jsonl \
    ./internal/observability/duckdb

RUN go test -tags duckdb ./internal/observability/duckdb
RUN go test -tags duckdb ./internal/olap
RUN go test -tags duckdb internal/web/olap.go internal/web/olap_test.go internal/web/olap_integration_duckdb_test.go
RUN export CGO_CFLAGS="$(/opt/zentao/php/bin/php-config --includes)" \
    && export CGO_LDFLAGS="-L/opt/zentao/php/lib -Wl,-rpath,\$ORIGIN/../php/lib,--disable-new-dtags -lphp $(/opt/zentao/php/bin/php-config --ldflags) $(/opt/zentao/php/bin/php-config --libs)" \
    && go build \
        -tags "duckdb nobadger nomysql nopgx nowatcher nobrotli nomercure" \
        -ldflags "-s -w -X main.runtimeVersion=runtime -X main.frankenPHPVersion=${FRANKENPHP_VERSION} -X main.caddyVersion=${CADDY_VERSION}" \
        -o /opt/zentao/bin/zentao \
        ./cmd/zentao-runtime

RUN bash /build/bundle-runtime-libs.sh /opt/zentao
RUN patchelf --force-rpath --set-rpath '$ORIGIN/../lib' /opt/zentao/php/bin/php

RUN export LD_LIBRARY_PATH=/opt/zentao/php/lib \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'exit(extension_loaded("pdo_dm") && in_array("dm", PDO::getAvailableDrivers(), true) ? 0 : 1);' \
    && /opt/zentao/php/bin/php -c /tmp/php.ini -r 'try { new PDO("dm:host=127.0.0.1;port=5236", "SYSDBA", "x", [PDO::ATTR_TIMEOUT=>2]); exit(2); } catch (Throwable $e) { $m = $e->getMessage(); exit((strpos($m, "-70089") !== false || stripos($m, "is missing") !== false || stripos($m, "Unable to load") !== false) ? 3 : 0); }'

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
COPY packaging/runtime/dm_svc.conf /etc/dm_svc.conf
COPY --chmod=0755 packaging/docker/healthcheck.sh /opt/zentao/packaging/docker/healthcheck.sh
ENV PATH="/opt/zentao/bin:/opt/zentao/php/bin:${PATH}"
ENTRYPOINT ["/opt/zentao/bin/zentao"]

FROM scratch AS artifact
COPY --from=builder /opt/zentao /
