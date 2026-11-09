FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /ms-notificacoes .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
COPY --from=build /ms-notificacoes /usr/local/bin/ms-notificacoes
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["ms-notificacoes"]
