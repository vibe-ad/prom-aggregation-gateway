FROM alpine:3.14.1
RUN apk add libc6-compat
COPY prom-aggregation-gateway /
ENTRYPOINT ["/prom-aggregation-gateway"]
