# syntax = docker/dockerfile:1.0-experimental

FROM ghcr.io/hmdlabs/hmd-ms-base:0.2.260

COPY meta-data/ /meta-data
COPY src/python/ /src/python
COPY ./src/docker/requirements.txt ${FUNCTION_DIR}

RUN --mount=type=secret,id=pipconfig,dst=/etc/pip.conf \
    pip install -r requirements.txt

COPY ./src/docker/prepare_workflow_gate.py /
COPY ./src/docker/collect_gate_results.py /
COPY ./src/docker/bender_session_call.py /

ENV PYTHONPATH "/usr/local/lib/python3.11/site-packages"
