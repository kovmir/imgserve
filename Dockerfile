FROM docker.io/library/golang:alpine AS builder

# Git repository is excluded from the container,
# therefore pass version via CLI args.
ARG GIT_VERSION="unknown"

WORKDIR /app

COPY . .

RUN apk upgrade
RUN apk add make git

RUN go version
RUN make GIT_VERSION=$GIT_VERSION

FROM docker.io/library/alpine:latest AS runtime

RUN apk upgrade

RUN adduser -D -s /sbin/nologin appuser

WORKDIR /home/appuser
COPY --from=builder /app/imgserve .

RUN chown appuser:appuser imgserve
RUN chmod 755 imgserve
RUN mkdir /data
RUN chown appuser:appuser /data

USER appuser

EXPOSE 8077

VOLUME ["/data"]

CMD ["./imgserve", "-dir", "/data"]
