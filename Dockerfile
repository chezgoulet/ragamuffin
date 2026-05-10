# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ragamuffin ./cmd/ragamuffin

# Stage 2: Runtime
FROM scratch

COPY --from=builder /build/ragamuffin /ragamuffin

EXPOSE 8000
ENTRYPOINT ["/ragamuffin"]
