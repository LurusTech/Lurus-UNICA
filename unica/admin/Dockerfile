FROM golang:1.25-alpine AS builder
# The context holds only pkg/ and this service, so the go.work that lists every
# module cannot load; each go.mod already replaces the shared pkg locally.
ENV GOWORK=off
WORKDIR /app
COPY go.work ./
COPY pkg/ pkg/
COPY admin/ admin/
RUN cd admin && go build -o /bin/admin ./cmd/admin

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/admin /bin/admin
ENTRYPOINT ["/bin/admin"]
