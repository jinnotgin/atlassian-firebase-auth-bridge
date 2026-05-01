# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/auth-bridge ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=build /out/auth-bridge /auth-bridge

USER nonroot:nonroot

ENV PORT=8080

EXPOSE 8080

ENTRYPOINT ["/auth-bridge"]