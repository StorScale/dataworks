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
RUN /opt/dlt/bin/pip install --no-cache-dir "dlt[filesystem,pyiceberg,sql-database]==1.30.0" pyiceberg==0.12.0 pandas==3.0.6 openpyxl==3.1.5 \
      psycopg2-binary==2.9.10 pymysql==1.2.3 simple-salesforce==1.12.10
# The platform's connectors (use case 8.4): dlt's verified sources, copied
# (sources/README.md), and the module that makes an import's resources of them.
COPY sources /opt/dlt/sources
# An import's run (the DAG file storscale_imports.py starts it here).
COPY storscale_import_run.py /opt/dlt/storscale_import_run.py
# Deliveries (use case 9.1): the files written (pyarrow), encrypted (gpg) and
# sent (SFTP, S3), in a virtualenv of their own; the run in it.
USER root
RUN apt-get update && apt-get install -y --no-install-recommends gnupg && rm -rf /var/lib/apt/lists/* \
    && python -m venv /opt/deliver && chown -R airflow:0 /opt/deliver
USER airflow
RUN /opt/deliver/bin/pip install --no-cache-dir trino==0.340.0 pyarrow==25.0.1 paramiko==5.0.0 boto3==1.43.108 requests==2.34.2
COPY storscale_delivery_run.py /opt/deliver/storscale_delivery_run.py
# Notebooks on a schedule (use case 7.7): papermill runs them in a virtualenv
# with the notebook image's packages (storscale.notebook starts it), and
# buckets_lake signed in as the project's pipelines.
USER root
RUN python -m venv /opt/notebook && chown -R airflow:0 /opt/notebook
USER airflow
RUN /opt/notebook/bin/pip install --no-cache-dir papermill==2.7.0 ipykernel==7.4.0 nbconvert==7.17.1 \
      boto3==1.43.108 requests==2.34.2 pandas==3.0.6 pyarrow==25.0.1
COPY notebook/buckets_lake.py /opt/notebook/lib/buckets_lake.py
RUN echo /opt/notebook/lib > "$(/opt/notebook/bin/python -c 'import sysconfig; print(sysconfig.get_paths()["purelib"])')/buckets.pth"
# dbt (use case 7.5), for projects' DAGs: in a virtualenv of its own too, run by
# the platform's helper (storscale.dbt), which every DAG can import.
USER root
RUN python -m venv /opt/dbt && chown -R airflow:0 /opt/dbt
COPY storscale /opt/storscale/storscale
# The platform's own DAGs for every Airflow (a merge request's review): the bundle "storscale".
COPY dags /opt/storscale/dags
RUN echo /opt/storscale > "$(python -c 'import sysconfig; print(sysconfig.get_paths()["purelib"])')/storscale.pth"
USER airflow
RUN /opt/dbt/bin/pip install --no-cache-dir dbt-core==1.12.5 dbt-trino==1.10.5
