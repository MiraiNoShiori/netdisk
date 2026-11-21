FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /netdisk ./cmd/server
FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 netdisk
COPY --from=build /netdisk /usr/local/bin/netdisk
WORKDIR /app
RUN mkdir -p /app/data && chown netdisk:netdisk /app/data
USER netdisk
EXPOSE 8081
ENTRYPOINT ["netdisk"]
