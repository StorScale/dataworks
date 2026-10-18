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
# turns the person's own Keycloak sign-in into Buckets credentials; and
# Schedule, which makes a notebook a project's pipeline.
FROM quay.io/jupyter/base-notebook:hub-6.0.1 AS notebook
RUN pip install --no-cache-dir jupyterlab==4.5.10 boto3==1.43.108 requests==2.34.2 pandas==3.0.6 pyarrow==25.0.1
COPY buckets_lake.py /opt/buckets/buckets_lake.py
COPY schedule/storscale_schedule.py /opt/buckets/storscale_schedule.py
RUN echo /opt/buckets > "$(python -c 'import site; print(site.getsitepackages()[0])')/buckets.pth"
COPY --from=schedule /src/dist /opt/conda/share/jupyter/labextensions/storscale-schedule
COPY schedule/jupyter_server_config.d/storscale_schedule.json /opt/conda/etc/jupyter/jupyter_server_config.d/storscale_schedule.json
