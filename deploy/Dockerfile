# Build stage: a static binary.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/Jairanpo/flamingo/internal/cli.version=${VERSION}" \
      -o /out/flamingo ./cmd/flamingo

# Runtime stage. Debian rather than distroless because the server shells
# out to yt-dlp for catalog syncs; curl is for the healthcheck.
FROM debian:bookworm-slim
# yt-dlp's standalone build, pinned and checked against the release's own
# checksum list. YouTube changes often: bump the version when listings break.
ARG YTDLP_VERSION=2026.08.19
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && cd /tmp \
 && base=https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION} \
 && curl -fsSLO "$base/yt-dlp_linux" && curl -fsSLO "$base/SHA2-256SUMS" \
 && grep ' yt-dlp_linux$' SHA2-256SUMS | sha256sum -c - \
 && install -m 755 yt-dlp_linux /usr/local/bin/yt-dlp \
 && rm yt-dlp_linux SHA2-256SUMS \
 && useradd --system --uid 10001 --home /data flamingo \
 && mkdir -p /data/thumbs && chown -R flamingo /data
COPY --from=build /out/flamingo /usr/local/bin/flamingo
USER flamingo
EXPOSE 8420
ENTRYPOINT ["flamingo"]
CMD ["serve"]
