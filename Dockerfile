# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine3.23 AS source-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/mmmcp ./cmd/mmmcp && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/imagecheck ./scripts/imagecheck

FROM cgr.dev/chainguard/wolfi-base AS runtime-base
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 1000 mmmcp && \
    adduser -S -D -H -u 1000 -G mmmcp mmmcp && \
    install -d -o mmmcp -g mmmcp -m 0700 /var/lib/mmmcp
ENV XDG_DATA_HOME=/var/lib
WORKDIR /var/lib/mmmcp
VOLUME ["/var/lib/mmmcp"]
EXPOSE 8080
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/mmmcp"]

# GoReleaser supplies one prebuilt binary under each TARGETPLATFORM directory.
FROM runtime-base AS runtime-release
ARG TARGETPLATFORM
COPY --chmod=0755 ${TARGETPLATFORM}/mmmcp /usr/local/bin/mmmcp

# The smoke script builds this target for a fixture on the Docker network.
FROM runtime-base AS imagecheck-source
COPY --from=source-build --chmod=0755 /out/imagecheck /usr/local/bin/imagecheck
ENTRYPOINT ["/usr/local/bin/imagecheck"]

# A normal `docker build .` uses the deterministic source build above.
FROM runtime-base AS runtime-source
COPY --from=source-build --chmod=0755 /out/mmmcp /usr/local/bin/mmmcp
