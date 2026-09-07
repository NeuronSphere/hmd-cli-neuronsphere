# syntax = docker/dockerfile:1.0-experimental

FROM ghcr.io/hmdlabs/hmd-ms-base:0.2.252

ARG FUNCTION_DIR
WORKDIR ${FUNCTION_DIR}

# Copy function code
RUN mkdir -p /app
COPY app/* ${FUNCTION_DIR}
COPY entry_script.sh ${FUNCTION_DIR}
RUN chmod +x ${FUNCTION_DIR}entry_script.sh

RUN --mount=type=secret,id=pipconfig,dst=/etc/pip.conf \
    pip install -r requirements.txt

ENV PYTHONPATH "/usr/local/lib/python3.11/site-packages"

# Override the hmd-ms-base default CMD — dbaccount has its own handler that
# manages a `FastAPIService({})` directly (via Mangum) rather than going
# through hmd_ms_base's config-driven dispatcher (which expects SERVICE_CONFIG
# with operations_modules; dbaccount has no such config).
CMD ["main.handler"]
