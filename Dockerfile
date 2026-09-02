FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/frigate-notifier ./cmd/frigate-notifier

FROM alpine:3.21
RUN addgroup -S app && adduser -S -G app app && mkdir /tmp/frigate-notifier && chown app:app /tmp/frigate-notifier
COPY --from=build /out/frigate-notifier /usr/local/bin/frigate-notifier
USER app
ENV TMPDIR=/tmp/frigate-notifier
ENTRYPOINT ["frigate-notifier"]
