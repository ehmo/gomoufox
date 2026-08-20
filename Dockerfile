FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -buildvcs=false \
    -ldflags "-s -w -buildid= -X github.com/ehmo/gomoufox/internal/buildinfo.Version=${VERSION}" \
    -o /out/gomoufox \
    ./cmd/gomoufox

FROM mcr.microsoft.com/playwright:v1.57.0-noble@sha256:8fb7af3bb488c51364d6554876a8eddf377736608327dbdf4177b4901faf7bc9

ARG VERSION=dev
LABEL org.opencontainers.image.title="gomoufox" \
      org.opencontainers.image.description="Guarded Camoufox browser daemon" \
      org.opencontainers.image.source="https://github.com/ehmo/gomoufox" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

ENV HOME=/opt/gomoufox \
    XDG_CACHE_HOME=/opt/gomoufox/.cache

COPY --from=build --chown=pwuser:pwuser /out/gomoufox /usr/local/bin/gomoufox

RUN install -d -o pwuser -g pwuser \
      /opt/gomoufox \
      /opt/gomoufox/.cache \
      /opt/gomoufox/.cache/fontconfig \
      /opt/gomoufox/.camoufox \
    && install -d -m 0700 -o pwuser -g pwuser /opt/gomoufox/sessions

USER pwuser
RUN gomoufox install \
    && gomoufox doctor

EXPOSE 3741

ENTRYPOINT ["gomoufox"]
CMD ["serve", "--bind", "0.0.0.0", "--session-dir", "/opt/gomoufox/sessions"]
