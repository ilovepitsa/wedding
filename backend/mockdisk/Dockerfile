FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY mockdisk/main.go ./mockdisk/main.go
RUN go build -ldflags="-s -w" -o mockdisk ./mockdisk

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/mockdisk .
RUN mkdir uploads
EXPOSE 9999
CMD ["./mockdisk"]
