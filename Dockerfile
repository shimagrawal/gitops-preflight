# syntax=docker/dockerfile:1
#
# gitops-preflight with everything it needs: the binary and flux-schema.
#
#   docker run --rm -v "$PWD:/work" ghcr.io/shimagrawal/gitops-preflight validate

ARG GO_VERSION=1.25
ARG FLUX_SCHEMA_VERSION=0.15.0

FROM golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/shimagrawal/gitops-preflight/internal/cli.Version=${VERSION}" \
      -o /out/gitops-preflight ./cmd/gitops-preflight

FROM alpine:3.22 AS flux-schema
ARG FLUX_SCHEMA_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl \
 && curl -fsSL "https://github.com/fluxcd/flux-schema/releases/download/v${FLUX_SCHEMA_VERSION}/flux-schema_${FLUX_SCHEMA_VERSION}_linux_${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin flux-schema

FROM alpine:3.22
# git: kustomize remote bases. ca-certificates: schema downloads.
RUN apk add --no-cache git ca-certificates \
 && adduser -D -u 65532 preflight
COPY --from=flux-schema /usr/local/bin/flux-schema /usr/local/bin/flux-schema
COPY --from=build /out/gitops-preflight /usr/local/bin/gitops-preflight
ENV GITOPS_PREFLIGHT_CACHE=/cache
RUN mkdir -p /cache && chown preflight /cache
USER 65532
WORKDIR /work
ENTRYPOINT ["gitops-preflight"]
CMD ["validate"]
