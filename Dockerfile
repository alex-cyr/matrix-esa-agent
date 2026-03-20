# multi-stage build builder
FROM golang:1.21-alpine as builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build the API binary
RUN go build -o esa-api ./cmd/api

# final lightweight image
FROM alpine:latest

WORKDIR /app

# Copy the compiled binary from builder
COPY --from=builder /app/esa-api /app/esa-api

# Crucial: Copy the framework dependencies for the runtime
COPY --from=builder /app/.agents /app/.agents
COPY --from=builder /app/knowledge /app/knowledge
COPY --from=builder /app/historical /app/historical

EXPOSE 8080

CMD ["/app/esa-api"]
