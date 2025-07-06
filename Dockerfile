FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o node-oos-operator .


# Final stage
FROM alpine:3

# Install ca-certificates for TLS
RUN apk --no-cache add ca-certificates && \
    addgroup -g 1001 -S operator && \
    adduser -S -u 1001 -G operator operator
WORKDIR /
COPY --from=builder --chown=1001:1001 /app/node-oos-operator /usr/local/bin/
USER 1001
EXPOSE 8080 
CMD ["/usr/local/bin/node-oos-operator"]