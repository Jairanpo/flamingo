# Build stage: a static binary.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/Jairanpo/yt/internal/cli.version=${VERSION}" \
      -o /out/yt ./cmd/yt

# Runtime stage. Debian rather than distroless because the server will shell
# out to yt-dlp for catalog syncs (phase 4); curl is for the healthcheck.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home /data yt \
 && mkdir -p /data/thumbs && chown -R yt /data
COPY --from=build /out/yt /usr/local/bin/yt
USER yt
EXPOSE 8420
ENTRYPOINT ["yt"]
CMD ["serve"]
