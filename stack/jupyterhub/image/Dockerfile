# Images for the JupyterHub example.
#
#   docker compose build      (compose.yaml builds the "hub" and "notebook" targets)

# JupyterHub 6, signing people in with Keycloak (OAuthenticator) and starting
# each person's notebook server in a container of its own (DockerSpawner), or
# in Kubernetes, a pod of its own (KubeSpawner).
FROM quay.io/jupyterhub/jupyterhub:6.0.1 AS hub
RUN pip install --no-cache-dir oauthenticator==17.4.0 dockerspawner==14.0.0 jupyterhub-kubespawner==7.1.0

# JupyterLab's Schedule (use case 7.7), built with JupyterLab's builder (it
# needs Node) for the JupyterLab the notebook server runs, 4.5.10.
FROM node:22.23.2-bookworm-slim AS schedule
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv && rm -rf /var/lib/apt/lists/* \
 && python3 -m venv /opt/lab && /opt/lab/bin/pip install --no-cache-dir jupyterlab==4.5.10
COPY schedule/package.json /src/
COPY schedule/lib /src/lib
WORKDIR /src
RUN npm install --no-audit --no-fund && /opt/lab/bin/jupyter labextension build .

# The notebook server people get: JupyterLab, boto3, and buckets_lake, which
# turns the person's own Keycloak sign-in into Buckets credentials; Schedule,
# which makes a notebook a project's pipeline; the libraries the common
# analyses and models need (use case 7.8); and VS Code in the browser
# (code-server, opened from JupyterLab's launcher through jupyter-server-proxy).
FROM quay.io/jupyter/base-notebook:hub-6.0.1 AS notebook
RUN pip install --no-cache-dir jupyterlab==4.5.10 boto3==1.43.108 requests==2.34.2 pandas==3.0.6 pyarrow==25.0.1 \
      scikit-learn==1.9.1 statsmodels==0.15.0 scipy==1.18.1 lightgbm==4.7.0 xgboost==3.4.1 flaml==2.7.0 mlflow-skinny==3.17.2 \
      geopandas==1.2.0 networkx==3.7 \
      jupyter-server-proxy==4.6.0 jupyter-codeserver-proxy==0.1.0
USER root
RUN mkdir -p /opt/code-server \
 && curl -fsSL https://github.com/coder/code-server/releases/download/v4.141.0/code-server-4.141.0-linux-amd64.tar.gz \
    | tar -xz -C /opt/code-server --strip-components=1 \
 && ln -s /opt/code-server/bin/code-server /usr/local/bin/code-server
USER ${NB_UID}
COPY buckets_lake.py /opt/buckets/buckets_lake.py
COPY schedule/storscale_schedule.py /opt/buckets/storscale_schedule.py
RUN echo /opt/buckets > "$(python -c 'import site; print(site.getsitepackages()[0])')/buckets.pth"
COPY --from=schedule /src/dist /opt/conda/share/jupyter/labextensions/storscale-schedule
COPY schedule/jupyter_server_config.d/storscale_schedule.json /opt/conda/etc/jupyter/jupyter_server_config.d/storscale_schedule.json
