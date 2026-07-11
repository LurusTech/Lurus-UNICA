FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY gateway/ gateway/
RUN cd gateway && go build -o /bin/gateway ./cmd/gateway

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/gateway /bin/gateway
ENTRYPOINT ["/bin/gateway"]
