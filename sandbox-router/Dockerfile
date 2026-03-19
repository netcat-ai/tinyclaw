FROM golang:1.26.1-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/sandbox-router ./cmd/sandbox-router

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/sandbox-router /sandbox-router

EXPOSE 8080

ENTRYPOINT ["/sandbox-router"]
