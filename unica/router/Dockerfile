FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY router/ router/
RUN cd router && go build -o /bin/router ./cmd/router

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/router /bin/router
ENTRYPOINT ["/bin/router"]
