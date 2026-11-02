# Writes the managed database constants to wp-config.php.

def post_composer(ctx):
    return {"actions": [{
        "type": "php-constants",
        "path": "wp-config.php",
        "fallback": "wp-config-sample.php",
        "fallback-template-file": "wordpress-config.php",
        "insert-before": "DB_CHARSET",
        "values": {
            "DB_NAME": {"from": "database.name"},
            "DB_USER": {"from": "database.user"},
            "DB_PASSWORD": {"from": "database.password"},
            "DB_HOST": {"format": {
                "kind": "host-port",
                "host": {"from": "database.host"},
                "port": {"from": "database.port"},
                "omit-port": "3306",
            }},
        },
        "order": ["DB_NAME", "DB_USER", "DB_PASSWORD", "DB_HOST"],
    }]}
