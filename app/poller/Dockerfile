FROM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git
COPY . ./
RUN go mod tidy
RUN CGO_ENABLED=0 go build -o /out/poller .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/poller /app/poller
CMD ["/app/poller"]
