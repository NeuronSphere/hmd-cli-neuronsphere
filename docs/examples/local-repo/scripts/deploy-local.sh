#!/bin/sh
set -eu

mkdir -p target
printf 'Deployed instance %s from %s@%s\n' \
  "$HMD_INSTANCE_NAME" "$HMD_REPO_NAME" "$HMD_REPO_VERSION" \
  > target/nsctl-demo.txt
cat target/nsctl-demo.txt
