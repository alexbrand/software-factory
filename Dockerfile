FROM golang:1.25 AS builder

WORKDIR /workspace

# Cache dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# Build all binaries.
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/controller-manager ./cmd/controller-manager
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/apiserver ./cmd/apiserver
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/bridge ./cmd/bridge

# Controller Manager image.
FROM gcr.io/distroless/static:nonroot AS controller-manager
WORKDIR /
COPY --from=builder /workspace/bin/controller-manager .
USER 65532:65532
ENTRYPOINT ["/controller-manager"]

# API Server image.
FROM gcr.io/distroless/static:nonroot AS apiserver
WORKDIR /
COPY --from=builder /workspace/bin/apiserver .
USER 65532:65532
ENTRYPOINT ["/apiserver"]

# Bridge image.
FROM gcr.io/distroless/static:nonroot AS bridge
WORKDIR /
COPY --from=builder /workspace/bin/bridge .
USER 65532:65532
ENTRYPOINT ["/bridge"]
