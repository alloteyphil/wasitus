# One image recipe for every Go program in the lab. Pick the program with
# --build-arg APP=..., for example APP=services/wallet or APP=mockpay.
# compose.yaml builds each lab service from this file.

# Stage 1: compile a static binary. Copying go.mod and go.sum first lets
# Docker reuse the downloaded modules when only the code changes.
FROM golang:1.27.4-alpine AS build
ARG APP
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test -n "$APP" && CGO_ENABLED=0 go build -trimpath -o /out/app "./$APP"

# Stage 2: a small runtime image. Alpine keeps busybox wget for health
# checks; the program runs as an unprivileged user.
FROM alpine:3.23
RUN adduser -D -u 10001 lab
USER lab
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
