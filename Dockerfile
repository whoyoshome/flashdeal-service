# syntax=docker/dockerfile:1

# Build stage: static binary, no cgo, reproducible paths.
FROM golang:1.27.2-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/flashdeal ./cmd/api

# Runtime stage: no shell, no package manager, non-root user.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/flashdeal /flashdeal
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/flashdeal"]
