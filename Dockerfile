# SPDX-License-Identifier: GPL-3.0-or-later
# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /workspace

# Dependencies are vendored (offline builds): no `go mod download`, go build uses vendor/ automatically.
COPY go.mod go.sum ./
COPY vendor/ vendor/

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -o manager ./cmd/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -o api-server ./cmd/api/

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/api-server .

USER 65532:65532

# Default entrypoint is the operator. The REST API deployment overrides it with /api-server.
ENTRYPOINT ["/manager"]
