FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o node-oos-operator .

# Final stage
FROM alpine:3.18

# Install ca-certificates for TLS
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1001 -S operator && \
    adduser -S -u 1001 -G operator operator

WORKDIR /

# Copy the binary from builder
COPY --from=builder /app/node-oos-operator .

# Change ownership
RUN chown operator:operator node-oos-operator

# Switch to non-root user
USER operator

# Expose metrics port
EXPOSE 8080

# Run the binary
ENTRYPOINT ["./node-oos-operator"]