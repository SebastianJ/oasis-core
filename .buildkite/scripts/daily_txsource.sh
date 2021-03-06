#! /bin/bash

set -euxo pipefail

# Script invoked from .buildkite/longtests.pipeline.yml

if [[ $BUILDKITE_RETRY_COUNT == 0 ]]; then
    rm -rf /var/tmp/longtests/*
    make
    ./.buildkite/scripts/test_e2e.sh -t txsource-multi
else
    curl -H "Content-Type: application/json" \
        -X POST \
        --data "{\"text\": \"Daily transaction source tests failure\"}" \
        "$SLACK_WEBHOOK_URL"

    # Exit with non-zero exit code, so that the buildkite build will be
    # marked as failed.
    exit 1
fi
