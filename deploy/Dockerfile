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
# The same program for every computer that downloads, offered in the app
# (Downloads → set up this computer): it runs there as the agent.
RUN for t in linux/amd64/flamingo-linux-amd64 windows/amd64/flamingo-windows-amd64.exe \
             darwin/arm64/flamingo-macos-arm64 darwin/amd64/flamingo-macos-amd64; do \
      os=${t%%/*}; rest=${t#*/}; arch=${rest%%/*}; file=${rest#*/}; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X github.com/Jairanpo/flamingo/internal/cli.version=${VERSION}" \
        -o /out/agents/$file ./cmd/flamingo || exit 1; \
    done

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
COPY --from=build /out/agents /usr/local/share/flamingo/agents
USER flamingo
EXPOSE 8420
ENTRYPOINT ["flamingo"]
CMD ["serve"]
