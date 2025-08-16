# Build stage
FROM golang:1.25 AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/protector ./cmd/protector

# Run stage (distroless for small, secure image)
FROM gcr.io/distroless/base-debian12
COPY --from=build /bin/protector /protector
EXPOSE 8080
ENTRYPOINT ["/protector"]
