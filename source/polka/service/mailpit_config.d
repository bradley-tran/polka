module polka.service.mailpit_config;

import std.format : format;
import std.regex : matchFirst, regex;
import std.string : strip;

import polka.config;
import polka.service.context : toolMailpit;
import polka.support.net : joinHostPort;

enum string MailpitListenHost = "127.0.0.1";
enum int DefaultMailpitSMTPPort = 1025;
enum int DefaultMailpitUIPort = 8025;

/// Validates a `mailpit` service configuration block if present.
void validateMailpitConfig(const(MailpitConfig)* mailpit) @safe
{
    if (mailpit is null)
        return;
    if (mailpit.version_.strip().length == 0)
        throw new Exception("mailpit configuration requires version");
    validateServiceVersion(toolMailpit, mailpit.version_);
    if (mailpit.smtpPort != 0 && !validServicePort(mailpit.smtpPort))
        throw new Exception("mailpit smtp-port must be between 1 and 65535");
    if (mailpit.uiPort != 0 && !validServicePort(mailpit.uiPort))
        throw new Exception("mailpit ui-port must be between 1 and 65535");
}

/// Returns the configured SMTP port or `DefaultMailpitSMTPPort` (1025).
int effectiveMailpitSMTPPort(const(MailpitConfig)* mailpit) @safe pure nothrow @nogc
{
    if (mailpit is null || mailpit.smtpPort == 0)
        return DefaultMailpitSMTPPort;
    return mailpit.smtpPort;
}

/// Returns the configured UI port or `DefaultMailpitUIPort` (8025).
int effectiveMailpitUIPort(const(MailpitConfig)* mailpit) @safe pure nothrow @nogc
{
    if (mailpit is null || mailpit.uiPort == 0)
        return DefaultMailpitUIPort;
    return mailpit.uiPort;
}

/// Formats a loopback listen address for the given Mailpit port.
string mailpitAddress(int port) @safe
{
    return joinHostPort(MailpitListenHost, port);
}

private bool validServicePort(int port) @safe pure nothrow @nogc
{
    return port >= 1 && port <= 65535;
}

private auto validServiceVersionRe() @safe
{
    return regex(`^[A-Za-z0-9][A-Za-z0-9._-]*$`);
}

/// Validates a managed service version string.
void validateServiceVersion(string tool, string version_) @safe
{
    auto trimmed = version_.strip();
    if (trimmed.length == 0)
        throw new Exception(tool ~ " version cannot be empty");
    if (trimmed.matchFirst(validServiceVersionRe()).empty)
        throw new Exception(format(`invalid %s version "%s": use letters, numbers, dots, dashes, or underscores`, tool, version_));
}
