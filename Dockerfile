# Build stage
FROM golang:1.25 AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/protector ./cmd/protector

# Run stage (distroless for small, secure image)
FROM gcr.io/distroless/base-debian12

# Create config directory inside the image
WORKDIR /app

# Copy binary
COPY --from=build /bin/protector /protector

# Copy configs (YAML, etc.)
COPY configs ./configs

EXPOSE 8080
ENTRYPOINT ["/protector"]