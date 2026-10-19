# Ore v0 image: the static binary plus git (for the repository core) and the docker CLI
# (pipelines and deployments run as sibling containers through the mounted socket).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(cat VERSION 2>/dev/null || echo 0.0.1-dev)" -o /ore ./cmd/ore

FROM alpine:3
RUN apk add --no-cache git docker-cli ca-certificates tar
COPY --from=build /ore /usr/local/bin/ore
VOLUME /data
EXPOSE 7777
ENTRYPOINT ["ore"]
CMD ["serve", "--data", "/data", "--addr", ":7777"]
