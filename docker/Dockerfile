# Build the zipgo image straight from a published GitHub release — no Go
# toolchain, cross-compilation, or registry push needed. The CLI binary is
# fetched from the releases endpoint at build time, one asset per target arch
# (TARGETARCH is provided automatically by BuildKit).
FROM alpine:3.20

ARG TARGETARCH
ARG REPO=GabrielVidal1/zipgo
# Release tag to install; "latest" resolves the most recent GitHub release.
ARG ZIPGO_VERSION=latest

RUN apk add --no-cache ca-certificates wget

RUN set -eux; \
    if [ "$ZIPGO_VERSION" = "latest" ]; then \
      url="https://github.com/${REPO}/releases/latest/download/zipgo-linux-${TARGETARCH}"; \
    else \
      url="https://github.com/${REPO}/releases/download/${ZIPGO_VERSION}/zipgo-linux-${TARGETARCH}"; \
    fi; \
    wget -O /usr/local/bin/zipgo "$url"; \
    chmod +x /usr/local/bin/zipgo; \
    zipgo --version || true

ENV ZIPGO_DOMAINS_FOLDER=/domains
ENTRYPOINT ["zipgo", "serve"]
