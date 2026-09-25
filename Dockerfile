FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/tsmon ./cmd/tsmon

FROM alpine:3.24

RUN adduser -D -H -u 65532 tsmon
COPY --from=build /out/tsmon /usr/local/bin/tsmon
USER tsmon
ENTRYPOINT ["/usr/local/bin/tsmon"]
