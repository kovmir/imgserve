FROM docker.io/library/golang:1.25-alpine AS builder

RUN apk add --no-cache make

WORKDIR /app

COPY . .

# Git repository is excluded from the container,
# therefore pass version via CLI args.
ARG GIT_VERSION="unknown"
RUN go version && make GIT_VERSION=$GIT_VERSION


FROM docker.io/library/alpine:3.23 AS runner

WORKDIR /app
COPY --from=builder /app/imgserve .

# Create the non-root user and set permissions.
RUN addgroup -S appuser && \
    adduser -S -G appuser appuser && \
    chown appuser:appuser imgserve && \
    chmod 755 imgserve && \
    mkdir /data && \
    chown appuser:appuser /data

USER appuser

EXPOSE 8077

VOLUME ["/data"]

CMD ["./imgserve", "-dir", "/data"]
