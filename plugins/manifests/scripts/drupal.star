# Generates settings.polka.php and includes it from Drupal's settings.php, so
# managed credentials stay out of the file Drupal's installer writes to.

_MYSQL = {
    "namespace": "Drupal\\mysql\\Driver\\Database\\mysql",
    "driver": "mysql",
    "autoload": "core/modules/mysql/src/Driver/Database/mysql/",
}

_POSTGRESQL = {
    "namespace": "Drupal\\pgsql\\Driver\\Database\\pgsql",
    "driver": "pgsql",
    "autoload": "core/modules/pgsql/src/Driver/Database/pgsql/",
}

_DRIVERS = {
    "mysql": _MYSQL,
    "mariadb": _MYSQL,
    "postgresql": _POSTGRESQL,
}

def post_composer(ctx):
    values = dict(_DRIVERS[ctx.database.engine])
    values.update({
        "database": {"from": "database.name"},
        "username": {"from": "database.user"},
        "password": {"from": "database.password"},
        "host": {"from": "database.host"},
        "port": {"from": "database.port"},
    })
    return {"actions": [
        {
            "type": "write-file",
            "base": "docroot",
            "path": "sites/default/settings.polka.php",
            "template-file": "drupal-settings.php",
            "encoding": "php-literal",
            "values": values,
        },
        {
            "type": "php-include",
            "base": "docroot",
            "path": "sites/default/settings.php",
            "fallback": "sites/default/default.settings.php",
            "include": "settings.polka.php",
            "marker": "settings-polka",
        },
    ]}
