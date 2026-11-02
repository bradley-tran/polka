# Writes the managed database URL to Symfony's .env.local.

def post_composer(ctx):
    return {"actions": [{
        "type": "dotenv",
        "path": ".env.local",
        "values": {"DATABASE_URL": {"from": "runtime-env.DATABASE_URL"}},
    }]}
