# Airflow 3.3 with the Trino client, for the pipelines' queries. The image
# already has the FAB auth manager (Keycloak sign-in through Authlib) and boto3.
FROM apache/airflow:3.3.2
# The git provider's DAG bundles take projects' pipelines from their repos.
# 1.0.0 and later never store a token on disk in a clone's git config.
RUN pip install --no-cache-dir trino==0.340.0 apache-airflow-providers-git==1.0.0
