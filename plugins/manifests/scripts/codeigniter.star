# Writes the managed database settings to CodeIgniter's .env.

_KEYS = [
    "database.default.hostname",
    "database.default.port",
    "database.default.database",
    "database.default.username",
    "database.default.password",
    "database.default.DBDriver",
]

def post_composer(ctx):
    return {"actions": [{
        "type": "dotenv",
        "path": ".env",
        "fallback": "env",
        "values": {key: {"from": "runtime-env." + key} for key in _KEYS},
        "order": _KEYS,
    }]}
