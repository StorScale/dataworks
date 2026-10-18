# Airflow 3.3 with the Trino client, for the pipelines' queries. The image
# already has the FAB auth manager (Keycloak sign-in through Authlib) and boto3.
FROM apache/airflow:3.3.2
# The git provider's DAG bundles take projects' pipelines from their repos.
# 1.0.0 and later never store a token on disk in a clone's git config.
RUN pip install --no-cache-dir trino==0.340.0 apache-airflow-providers-git==1.0.0
# Projects' imports (use case 8.2): dlt, writing Iceberg through pyiceberg to
# Nessie's REST catalog. In a virtualenv of its own, beside Airflow's
# (whose pinned packages it would fight): the imports' DAGs run it with
# ExternalPythonOperator.
USER root
RUN python -m venv /opt/dlt && chown -R airflow:0 /opt/dlt
USER airflow
RUN /opt/dlt/bin/pip install --no-cache-dir "dlt[filesystem,pyiceberg,sql-database]==1.30.0" pyiceberg==0.12.0 \
      psycopg2-binary==2.9.10 pymysql==1.2.3
# An import's run (the DAG file storscale_imports.py starts it here).
COPY storscale_import_run.py /opt/dlt/storscale_import_run.py
