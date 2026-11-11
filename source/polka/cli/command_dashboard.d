module polka.cli.command_dashboard;

// Live auto-refreshing status view (`polka dashboard`) for terminals using
// POSIX alternate-screen buffer + non-canonical `poll` loop, gracefully falling
// back to one-shot `polka status` output when stdin/stdout is non-interactive.

import std.datetime : Clock, UTC;
import std.exception : collectException;

import polka.backend : FileTextWriter, Store, TextWriter, writeOut;
import polka.cli.command_env : runStatus;
import polka.cli.dashboard_snapshot :
    dashboardPollInterval,
    gatherDashboardSnapshot,
    renderDashboard;
import polka.cli.spinner : isTTY, stdinIsTTY;
import polka.cli.types : CliStatusException;

version (Posix)
{
    import core.sys.posix.poll : poll, pollfd, POLLIN;
    import core.sys.posix.termios : ECHO, ICANON, ISIG, TCSANOW, tcgetattr, tcsetattr, termios, VMIN, VTIME;
    import core.sys.posix.unistd : read_ = read, STDIN_FILENO;
}

/// Runs the live dashboard loop when attached to an interactive terminal, or
/// degrades to one-shot `runStatus` output when piped/redirected.
void runDashboard(TextWriter stdout, TextWriter stderr, ref Store store) @trusted
{
    if (!isTTY(stdout) || !stdinIsTTY())
    {
        runStatus(stdout, stderr, store);
        return;
    }

    version (Posix)
    {
        termios origTerm;
        if (tcgetattr(STDIN_FILENO, &origTerm) != 0)
            throw new CliStatusException(1, "read terminal attributes failed");

        termios rawTerm = origTerm;
        rawTerm.c_lflag &= ~(ICANON | ECHO | ISIG);
        rawTerm.c_cc[VMIN] = 1;
        rawTerm.c_cc[VTIME] = 0;
        if (tcsetattr(STDIN_FILENO, TCSANOW, &rawTerm) != 0)
            throw new CliStatusException(1, "enable raw terminal mode failed");

        writeOut(stdout, "\033[?1049h\033[?25l");
        flushWriter(stdout);
        scope (exit)
        {
            writeOut(stdout, "\033[?25h\033[?1049l");
            flushWriter(stdout);
            tcsetattr(STDIN_FILENO, TCSANOW, &origTerm);
        }

        auto timeoutMs = cast(int) dashboardPollInterval.total!"msecs";
        while (true)
        {
            auto snapshot = gatherDashboardSnapshot(store, Clock.currTime(UTC()));
            writeOut(stdout, "\033[H\033[2J");
            writeOut(stdout, renderDashboard(snapshot));
            flushWriter(stdout);

            // Wait up to `dashboardPollInterval` for quit keys (`q`, `Esc`, `Ctrl+C`).
            pollfd pfd;
            pfd.fd = STDIN_FILENO;
            pfd.events = POLLIN;
            int ready = poll(&pfd, 1, timeoutMs);
            if (ready > 0 && (pfd.revents & POLLIN) != 0)
            {
                ubyte[1] key;
                if (read_(STDIN_FILENO, key.ptr, 1) <= 0)
                    return;

                if (key[0] == 'q' || key[0] == 'Q' || key[0] == 0x03)
                    return;

                if (key[0] == 0x1b)
                {
                    // Drain any trailing ANSI sequence bytes; standalone Esc quits.
                    pollfd escPfd;
                    escPfd.fd = STDIN_FILENO;
                    escPfd.events = POLLIN;
                    if (poll(&escPfd, 1, 20) <= 0 || (escPfd.revents & POLLIN) == 0)
                        return;
                    ubyte[8] sink;
                    read_(STDIN_FILENO, sink.ptr, sink.length);
                }
            }
        }
    }
    else
    {
        // Fallback when compiled for non-POSIX platforms without termios.
        runStatus(stdout, stderr, store);
    }
}

private void flushWriter(TextWriter w) @trusted
{
    if (auto fw = cast(FileTextWriter) w)
        collectException(fw.streamFile().flush());
}
