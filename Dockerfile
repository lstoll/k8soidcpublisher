# syntax=docker/dockerfile:1.3

FROM golang:1.22-bookworm AS build

RUN mkdir -p /src/clusterpub
WORKDIR /src/clusterpub

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go install ./...

FROM debian:bookworm

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /go/bin/clusterpub /usr/bin/clusterpub

ENTRYPOINT ["/usr/bin/clusterpub"]
