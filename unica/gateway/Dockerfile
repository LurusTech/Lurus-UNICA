FROM golang:1.25-alpine AS builder
# The context holds only pkg/ and this service, so the go.work that lists every
# module cannot load; each go.mod already replaces the shared pkg locally.
ENV GOWORK=off
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY gateway/ gateway/
RUN cd gateway && go build -o /bin/gateway ./cmd/gateway

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/gateway /bin/gateway
ENTRYPOINT ["/bin/gateway"]
