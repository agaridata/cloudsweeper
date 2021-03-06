# STEP 1 build executable binary
FROM golang:1.15-alpine as builder

ADD . /cloudsweeper
WORKDIR /cloudsweeper

RUN apk -U upgrade && \
    apk add --no-cache -U git ca-certificates gcc musl-dev && \
    update-ca-certificates && \
    go get ./... && \
    go test -cover ./... && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -ldflags="-w -s" -o /cs cmd/cloudsweeper/*.go

FROM scratch
COPY --from=builder /cs /cs
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/share/ca-certificates/* /usr/share/ca-certificates/
ENTRYPOINT [ "/cs" ]
