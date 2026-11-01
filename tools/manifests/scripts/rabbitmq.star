# Broker-specific cross-field rules on top of the manifest's version
# constraint and port-range validation.

def validate(env):
    port = env.setting("port") or 5672
    management_port = env.setting("management-port") or 15672
    if port == management_port:
        fail("rabbitmq port and management port must be different")

    for credential in (env.setting("username"), env.setting("password")):
        if "\r" in credential or "\n" in credential:
            fail("rabbitmq credentials must be single-line values")
