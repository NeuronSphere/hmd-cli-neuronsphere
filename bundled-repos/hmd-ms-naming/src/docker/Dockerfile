# syntax = docker/dockerfile:1.0-experimental

FROM ghcr.io/hmdlabs/hmd-ms-base:0.2.252

COPY meta-data/ /meta-data
COPY src/ /src
COPY src/docker/requirements.txt ${FUNCTION_DIR}

RUN --mount=type=secret,id=pipconfig,dst=/etc/pip.conf \
  pip install -r requirements.txt

ENV PYTHONPATH "/usr/local/lib/python3.9/site-packages"
