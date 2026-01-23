FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bananasplit ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /build/bananasplit .
EXPOSE 3001
ENV PEEL_URL=""
ENV BANANAGINE_URL="http://bananagine:3000"
CMD ["./bananasplit"]