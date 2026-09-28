FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/tail-watch ./cmd/tail-watch

FROM alpine:3.24

RUN adduser -D -H -u 65532 tail-watch
COPY --from=build /out/tail-watch /usr/local/bin/tail-watch
USER tail-watch
ENTRYPOINT ["/usr/local/bin/tail-watch"]
