# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.6
FROM golang:${GO_VERSION} AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/Sneakers-PAM/sneakers-notify/internal/buildinfo.Version=${VERSION} -X github.com/Sneakers-PAM/sneakers-notify/internal/buildinfo.Commit=${COMMIT}" -o /out/notify ./cmd/notify

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/notify /notify
USER nonroot:nonroot
ENTRYPOINT ["/notify"]
