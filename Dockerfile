# Binaries are cross-compiled on the host (see `make docker-binaries`) and
# copied in per-arch. Nothing arch-specific runs during the image build, so
# multi-arch `docker buildx` needs no QEMU emulation.

# ca-certificates bundle, fetched on the native build arch (no emulation).
FROM --platform=$BUILDPLATFORM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates

FROM alpine:3.20
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ARG TARGETARCH
COPY dist/zipgo-linux-$TARGETARCH /usr/local/bin/zipgo
ENV ZIPGO_DOMAINS_FOLDER=/domains
ENTRYPOINT ["zipgo", "serve"]
