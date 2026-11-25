# The Kedalio core's image: one static Go binary on :8787 behind a TLS proxy, its database and instance key in one
# volume (/data), no C toolchain (the SQLite driver is pure Go).
# Built with the repository's root as context:
#   docker build -f core/image/Dockerfile -t kedalio-core:local .
# CI builds it on every green push; compose.yaml runs it, and deploy/coolify/core.yaml is an example for Coolify.
# Images pinned by digest: a tag can be moved, a digest cannot (refresh the tag and the digest together).

# the build stage runs on the builder's own architecture and cross-compiles (no emulated compiler for a multi-arch image)
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
# the module cache first, so a change to the code does not fetch the dependencies again
COPY go.mod go.sum ./
RUN go mod download
COPY protocol protocol
COPY core core
# the version `kedalio-core -version` prints; the image workflow passes it (a plain build says dev)
ARG VERSION=dev
ARG TARGETOS TARGETARCH
# timetzdata: the time zone is an IANA name, and the runtime image carries no zoneinfo
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -ldflags="-s -w -X main.version=${VERSION}" -o /out/kedalio-core ./core/cmd/kedalio-core \
  && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -ldflags="-s -w" -o /out/kedalio-restore ./core/cmd/kedalio-restore

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# CA certificates: the core calls the offsite backup store over TLS.
RUN apk add --no-cache ca-certificates \
  && adduser -D -u 10001 -h /data kedalio \
  && mkdir -p /data && chown kedalio:kedalio /data
COPY --from=build /out/kedalio-core /out/kedalio-restore /usr/local/bin/
# The core's folder is /data/core and not the volume's root, as the instance in production has it.
# The core listens on every interface here: the compose file's proxy is the only thing that reaches it, and
# KEDALIO_CORE_TOKEN (required) is set by the deployment, never in the image.
ENV KEDALIO_CORE_DATA=/data/core KEDALIO_CORE_LISTEN=0.0.0.0:8787
VOLUME /data
EXPOSE 8787
USER kedalio
# /health answers {"status":"ok"}, or {"status":"locked"} (still 200) while it waits for the recovery code
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8787/health"]
ENTRYPOINT ["kedalio-core"]
