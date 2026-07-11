FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY admin/ admin/
RUN cd admin && go build -o /bin/admin ./cmd/admin

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/admin /bin/admin
ENTRYPOINT ["/bin/admin"]
