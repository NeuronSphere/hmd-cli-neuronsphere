# syntax = docker/dockerfile:1.0-experimental

FROM ghcr.io/hmdlabs/hmd-ms-librarian:0.5.500

COPY requirements.txt ${FUNCTION_DIR}

COPY ./content_item_types/ /content_item_types

RUN --mount=type=secret,id=pipconfig,dst=/etc/pip.conf \
    pip install -r requirements.txt

