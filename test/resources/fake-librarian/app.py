import json
import os


def handler(event, context):
    return {
        "statusCode": 200,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps({
            "service": "fake-librarian",
            "fake_bucket": os.environ.get("FAKE_BUCKET", ""),
        }),
    }
