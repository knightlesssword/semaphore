# Stage 1: build
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /semaphore ./cmd/semaphore

# Stage 2: minimal runtime image
FROM gcr.io/distroless/static-debian12

COPY --from=builder /semaphore /semaphore
COPY --from=builder /app/internal/config/config.yaml /etc/semaphore/config.yaml

EXPOSE 8080

ENTRYPOINT ["/semaphore"]
