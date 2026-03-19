FROM golang:1.26.1-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o /out/tinyclaw .

FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/tinyclaw /app/tinyclaw
COPY --from=builder /src/wecom/finance/lib /app/wecom/finance/lib

ENV LD_LIBRARY_PATH=/app/wecom/finance/lib

ENTRYPOINT ["/app/tinyclaw"]
