FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/http-relay .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget \
    && adduser -D -H -u 10001 relay

WORKDIR /app
COPY --from=build /out/http-relay /app/http-relay

USER relay
EXPOSE 3002

CMD ["/app/http-relay"]
