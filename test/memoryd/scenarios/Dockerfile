FROM golang:1.24.6-alpine3.22 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/memory .

FROM alpine:3.22

RUN adduser -D -u 10001 agent && mkdir -p /workspace && chown agent:agent /workspace
COPY --from=build --chown=10001:10001 /out/memory /usr/local/bin/memory

USER agent
WORKDIR /workspace
CMD ["sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"]
