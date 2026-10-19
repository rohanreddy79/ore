# Ore image: the static binary plus git (the repository core) and the docker CLI
# (pipelines and deployments run as sibling containers through the mounted socket).
# Multi-arch: build with  docker buildx build --platform linux/amd64,linux/arm64 -t ore:local -f deploy/Dockerfile .
# The Go cross-compile happens on the build platform; only the final stage is per target.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o /ore ./cmd/ore

FROM alpine:3
RUN apk add --no-cache git docker-cli ca-certificates tar openssh-client tzdata
COPY --from=build /ore /usr/local/bin/ore
VOLUME /data
EXPOSE 7777
ENV ORE_DATA=/data ORE_ADDR=:7777
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:7777/healthz >/dev/null || exit 1
ENTRYPOINT ["ore"]
CMD ["serve"]
