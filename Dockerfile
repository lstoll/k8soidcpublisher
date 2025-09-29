# syntax=docker/dockerfile:1.3

FROM golang:1-trixie AS build

RUN mkdir -p /src/k8soidcpublisher
WORKDIR /src/k8soidcpublisher

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go install ./...

FROM debian:trixie

COPY --from=build /go/bin/k8soidcpublisher /usr/bin/k8soidcpublisher

ENTRYPOINT ["/usr/bin/k8soidcpublisher"]
