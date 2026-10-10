FROM golang:1.24.6-alpine3.22 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/domain-world ./testdata/memoryd/domainops/cmd/domain-world && \
    CGO_ENABLED=0 go build -trimpath -o /out/domain-load ./testdata/memoryd/domainops/cmd/domain-load && \
    CGO_ENABLED=0 go build -trimpath -o /out/domainctl ./testdata/memoryd/domainops/cmd/domainctl && \
    CGO_ENABLED=0 go build -trimpath -o /out/memory .

FROM alpine:3.22 AS world

RUN adduser -D -u 10001 operator
COPY --from=build --chown=10001:10001 /out/domain-world /usr/local/bin/domain-world
COPY --from=build --chown=10001:10001 /out/domain-load /usr/local/bin/domain-load
COPY --from=build --chown=10001:10001 /out/domainctl /usr/local/bin/domainctl

USER operator
ENTRYPOINT ["domain-world"]

FROM node:22-alpine3.22 AS agent

ARG PI_VERSION=0.83.0
RUN apk add --no-cache bash ca-certificates jq sqlite && \
    npm install -g --ignore-scripts --no-audit --no-fund \
      "@earendil-works/pi-coding-agent@${PI_VERSION}" && \
    adduser -D -u 10001 agent && mkdir -p /workspace && chown agent:agent /workspace
COPY --chown=10001:10001 test/memoryd/runtime/pi/delegate.ts \
  test/memoryd/runtime/pi/delegate-runtime.mjs /opt/memory/pi-delegate/
COPY --from=build --chown=10001:10001 /out/domainctl /usr/local/bin/domainctl
COPY --from=build --chown=10001:10001 /out/memory /usr/local/bin/memory

USER agent
WORKDIR /workspace
CMD ["sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"]
