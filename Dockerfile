# syntax=docker/dockerfile:1

ARG GO_VERSION=1.24.6

FROM golang:${GO_VERSION}-bookworm AS dev
RUN apt-get update \
  && apt-get install -y --no-install-recommends bash git jq make ca-certificates \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["bash"]

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
  -ldflags "-s -w -X github.com/JayWanX/memory/cmd.version=${VERSION}" \
  -o /out/memory .

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
  && addgroup -S memory \
  && adduser -S -G memory -h /home/memory memory \
  && mkdir -p /memory \
  && chown -R memory:memory /memory /home/memory
COPY --from=build /out/memory /usr/local/bin/memory
USER memory
ENV MEMORY_DATA_DIR=/memory \
    MEMORY_STORE=default
VOLUME ["/memory"]
ENTRYPOINT ["memory"]
CMD ["status"]
