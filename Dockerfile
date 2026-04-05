FROM docker.io/library/golang:alpine AS builder

# Git repository is excluded from the container,
# therefore pass version via CLI args.
ARG GIT_VERSION="unknown"

WORKDIR /app

COPY . .

RUN apk add --no-cache make
RUN go version && make GIT_VERSION=$GIT_VERSION


FROM docker.io/library/alpine:latest AS runner

WORKDIR /app
COPY --from=builder /app/imgserve .

RUN addgroup -S appuser && \
    adduser -S -G appuser appuser
RUN chown appuser:appuser imgserve && \
    chmod 755 imgserve
RUN mkdir /data && \
    chown appuser:appuser /data

USER appuser

EXPOSE 8077

VOLUME ["/data"]

CMD ["./imgserve", "-dir", "/data"]
