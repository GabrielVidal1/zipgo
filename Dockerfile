FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /zipgo .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /zipgo /usr/local/bin/zipgo
ENV ZIPGO_DOMAINS_FOLDER=/domains
ENTRYPOINT ["zipgo", "serve"]
