FROM oven/bun:1.4.2-alpine AS css

WORKDIR /src
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY css ./css
COPY tmpl ./tmpl
RUN bun run build:css

FROM golang:1.27-alpine AS builder

RUN mkdir -p /app/bin
WORKDIR /src
COPY . .
COPY --from=css /src/static/css/base.css ./static/css/base.css
ENV CGO_ENABLED=0
RUN go build -o /app/bin/what .

FROM alpine:3.24
COPY --from=builder /app/bin/what /app/bin/what

CMD ["/app/bin/what"]
