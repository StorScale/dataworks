# Setup and test runner for the examples: mc (MinIO publishes no images any
# more, so it is built from source), the Trino and Spark Connect Python
# clients, boto3, openssl for Trino's self-signed certificate, and htpasswd
# for Trino's password file; git, for the git host the pipelines_git suite serves.
FROM golang:1.24-bookworm AS mc
RUN CGO_ENABLED=0 go install github.com/minio/mc@v0.0.0-20251106162529-77f82e18b540

FROM python:3.12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssl curl apache2-utils git \
 && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir trino==0.340.0 boto3==1.43.108 requests==2.34.2 pyspark-client==4.1.3 websocket-client==1.9.2 mcp==2.3.0 psycopg2-binary==2.9.10
COPY --from=mc /go/bin/mc /usr/local/bin/mc
# Kubernetes runs it as an unprivileged user (65532): a home, and a working
# directory, any user may write to.
ENV HOME=/tmp
RUN mkdir -p /work && chmod 1777 /work
WORKDIR /work
