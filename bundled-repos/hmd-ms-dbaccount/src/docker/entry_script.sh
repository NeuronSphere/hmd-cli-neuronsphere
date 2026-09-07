#!/bin/sh
if [ -z "${AWS_LAMBDA_RUNTIME_API}" ]; then
  exec ./aws-lambda-rie /usr/local/bin/python3.11 -m awslambdaric "$@"
else
  exec /usr/local/bin/python3.11 -m awslambdaric "$@"
fi