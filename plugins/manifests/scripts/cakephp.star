# Points every CakePHP datasource at the managed database.

_DRIVERS = {
    "mysql": "Cake\\Database\\Driver\\Mysql",
    "mariadb": "Cake\\Database\\Driver\\Mysql",
    "postgresql": "Cake\\Database\\Driver\\Postgres",
}

_ENCODINGS = {
    "mysql": "utf8mb4",
    "mariadb": "utf8mb4",
    "postgresql": "utf8",
}

_DATASOURCES = ["default", "test", "debug_kit"]

_ORDER = ["className", "driver", "host", "port", "username", "password", "database", "encoding", "url"]

def post_composer(ctx):
    engine = ctx.database.engine
    values = {
        "className": "Cake\\Database\\Connection",
        "driver": _DRIVERS[engine],
        "host": {"from": "database.host"},
        "port": {"from": "database.port"},
        "username": {"from": "database.user"},
        "password": {"from": "database.password"},
        "database": {"from": "database.name"},
        "encoding": _ENCODINGS[engine],
        # A DATABASE_URL default in app_local.php would override the fields above.
        "url": None,
    }
    return {"actions": [{
        "type": "php-array",
        "path": "config/app_local.php",
        "fallback": "config/app_local.example.php",
        "fallback-template-file": "php-return-array.php",
        "updates": [
            {"path": ["Datasources", name], "values": values, "order": _ORDER}
            for name in _DATASOURCES
        ],
    }]}
