FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=unknown
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build \
  -ldflags="-s -w \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.Version=${VERSION}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.Commit=${COMMIT}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.BuildDate=${BUILD_DATE}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.GoVersion=go1.25.0'" \
  -o ragamuffin ./cmd/ragamuffin

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /src/ragamuffin /ragamuffin
EXPOSE 8000
ENTRYPOINT ["/ragamuffin"]
