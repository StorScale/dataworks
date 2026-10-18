# catalog-sync: OpenMetadata's ingestion framework, run by the platform
# (OpenMetadata's own Airflow isn't used).
FROM python:3.12-slim
# boto3 comes with it, at the version it needs.
RUN pip install --no-cache-dir "openmetadata-ingestion[trino,dbt]==2.0.4.0"
# Kubernetes runs it as an unprivileged user (65532): a home it may write to.
ENV HOME=/tmp
WORKDIR /app
COPY sync.py .
CMD ["python", "-u", "sync.py"]
