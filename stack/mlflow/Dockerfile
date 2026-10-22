# The platform's MLflow (use case 15.1): one tracking server and model
# registry for the install, with a workspace per project, reached only
# through platformd (/mlflow/<project>/), which signs people in and names
# the workspace. Its runs and models in Postgres; its artifacts in Buckets,
# proxied by the server (--serve-artifacts), so clients never hold the keys.
FROM python:3.12-slim
RUN pip install --no-cache-dir mlflow==3.17.2 psycopg2-binary==2.9.10 boto3==1.43.108
# No background job runner: it serves MLflow's GenAI features (scorers, trace
# rollups), which the platform doesn't use, and its worker processes and their
# queue in /dev/shm took MLflow past its memory limit at idle.
ENV MLFLOW_SERVER_ENABLE_JOB_EXECUTION=false
RUN useradd --uid 10001 --create-home mlflow
# Its job runner (huey workers, for MLflow's GenAI scorers and trace rollups) is
# off: the platform uses none of it, and it alone took the pod past its memory.
ENV MLFLOW_SERVER_ENABLE_JOB_EXECUTION=false
USER 10001
WORKDIR /home/mlflow
EXPOSE 5000
CMD ["sh", "-c", "exec mlflow server --host 0.0.0.0 --port 5000 --workers 1 --backend-store-uri \"postgresql+psycopg2://mlflow:${MLFLOW_DB_PASSWORD}@${MLFLOW_DB_HOST:-mlflow-db}:5432/mlflow\" --serve-artifacts --artifacts-destination s3://mlflow"]
