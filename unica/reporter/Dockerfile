FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY reporter/ reporter/
RUN cd reporter && go build -o /bin/reporter ./cmd/reporter

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/reporter /bin/reporter
ENTRYPOINT ["/bin/reporter"]
