# Build the zipgo image straight from a published GitHub release — no Go
# toolchain, cross-compilation, or registry push needed. The CLI binary is
# fetched from the releases endpoint at build time, one asset per target arch
# (TARGETARCH is provided automatically by BuildKit), and checked against the
# release's checksums.txt before it is installed.
#
# The base image is pinned by digest; Dependabot (.github/dependabot.yml)
# proposes the bump when a new alpine lands.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

ARG TARGETARCH
ARG REPO=GabrielVidal1/zipgo
# Release tag to install; "latest" resolves the most recent GitHub release.
# Pin one in production (ZIPGO_VERSION=v1.11.0 docker compose build) so a
# rebuild never ships a release you did not choose.
ARG ZIPGO_VERSION=latest

RUN apk add --no-cache ca-certificates wget

RUN set -eux; \
    if [ "$ZIPGO_VERSION" = "latest" ]; then \
      base="https://github.com/${REPO}/releases/latest/download"; \
    else \
      base="https://github.com/${REPO}/releases/download/${ZIPGO_VERSION}"; \
    fi; \
    asset="zipgo-linux-${TARGETARCH}"; \
    cd /tmp; \
    wget -q "$base/$asset" "$base/checksums.txt"; \
    grep " ${asset}\$" checksums.txt | sha256sum -c -; \
    install -m 0755 "$asset" /usr/local/bin/zipgo; \
    rm -f "$asset" checksums.txt; \
    zipgo version

# Run unprivileged. A container can bind :80/:443 without root (Docker sets
# net.ipv4.ip_unprivileged_port_start=0 inside it), and nothing zipgo does
# needs a capability. uid 1000 is the usual owner of the mounted domains
# folder, so the back-office writes a site's .zipgoconfig.json the way
# `zipgo off` over SSH would. Certificates and the session key live in /data
# (Caddy's data dir, via XDG_DATA_HOME): mount a volume there.
RUN addgroup -g 1000 zipgo \
    && adduser -D -u 1000 -G zipgo -h /home/zipgo zipgo \
    && mkdir -p /data /domains \
    && chown zipgo:zipgo /data /domains

ENV ZIPGO_DOMAINS_FOLDER=/domains \
    XDG_DATA_HOME=/data \
    XDG_CONFIG_HOME=/data/config \
    HOME=/home/zipgo

VOLUME ["/data"]
USER zipgo
ENTRYPOINT ["zipgo", "serve"]
