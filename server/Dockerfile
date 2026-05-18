# syntax=docker/dockerfile:1.6

# === build stage ===
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/PeterGuy326/mem/server/internal/api.Version=${VERSION}" \
    -o /out/memd  ./cmd/memd  && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/mem   ./cmd/mem   && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/mem-mcp ./cmd/mem-mcp

# === runtime stage ===
FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/memd     /usr/local/bin/memd
COPY --from=build /out/mem      /usr/local/bin/mem
COPY --from=build /out/mem-mcp  /usr/local/bin/mem-mcp
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/memd"]
