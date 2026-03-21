FROM docker.io/library/golang:alpine AS builder

WORKDIR /app

COPY . .

RUN CGO_ENABLED='0' go build -o imgserv .

FROM docker.io/library/alpine:latest AS runtime

RUN apk upgrade

RUN adduser -D -s /sbin/nologin appuser

WORKDIR /home/appuser
COPY --from=builder /app/imgserv .

RUN chown appuser:appuser imgserv
RUN chmod 755 imgserv
RUN mkdir /data
RUN chown appuser:appuser /data

USER appuser

EXPOSE 8077

VOLUME ["/data"]

CMD ["./imgserv", "-dir", "/data"]
