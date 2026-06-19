FROM golang:1.22-alpine AS builder
ARG UPLOAD_MODE=simple
WORKDIR /app
COPY go.mod .
COPY . .
RUN go build -ldflags="-s -w" -o server .

FROM alpine:latest
ARG UPLOAD_MODE=simple
ENV UPLOAD_MODE=$UPLOAD_MODE
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]
