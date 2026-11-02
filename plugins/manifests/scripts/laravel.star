# Writes the managed database settings to Laravel's .env.

_KEYS = ["DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD"]

def post_composer(ctx):
    return {"actions": [{
        "type": "dotenv",
        "path": ".env",
        "fallback": ".env.example",
        "values": {key: {"from": "runtime-env." + key} for key in _KEYS},
        "order": _KEYS,
    }]}
