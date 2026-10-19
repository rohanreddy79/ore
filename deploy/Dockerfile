# Ore image: the static binary (with the web app embedded) plus git (the
# repository core) and the docker CLI (pipelines and deployments run as sibling
# containers through the mounted socket).
# Multi-arch: build with  docker buildx build --platform linux/amd64,linux/arm64 -t ore:local -f deploy/Dockerfile .
# The web app build and the Go cross-compile happen on the build platform; only
# the final stage is per target.

# 1. The web app: ui/ -> internal/webapp/dist (Vite writes there; see ui/vite.config.ts).
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# 2. The binary, embedding the web app from stage 1.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webapp/dist/ /src/internal/webapp/dist/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o /ore ./cmd/ore

# 3. The image. git-daemon carries git-http-backend on Alpine; `ore serve`
# refuses to start without it.
FROM alpine:3
RUN apk add --no-cache git git-daemon docker-cli ca-certificates tar openssh-client tzdata
COPY --from=build /ore /usr/local/bin/ore
VOLUME /data
EXPOSE 7777
ENV ORE_DATA=/data ORE_ADDR=:7777
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:7777/healthz >/dev/null || exit 1
ENTRYPOINT ["ore"]
CMD ["serve"]
