# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.6
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/notify ./cmd/notify

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/notify /notify
USER nonroot:nonroot
ENTRYPOINT ["/notify"]
