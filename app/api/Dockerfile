FROM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git
COPY . ./
RUN go mod tidy
RUN CGO_ENABLED=0 go build -o /out/api .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /src/go.mod /app/go.mod
COPY --from=builder /src/go.sum /app/go.sum
EXPOSE 8080
CMD ["/app/api"]
