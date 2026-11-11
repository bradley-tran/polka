module polka.cli.spinner;

// Terminal TTY detection and concurrent-install animated progress spinner.
// Falls back to clean line-by-line stage output when stdout is non-interactive.

import core.sync.condition : Condition;
import core.sync.mutex : Mutex;
import core.thread : Thread;
import core.time : Duration, dur;
import std.format : format;
import std.stdio : stdin;

import polka.backend :
    BufferTextWriter,
    FileTextWriter,
    InstallProgressDownloading,
    InstallProgressInstalled,
    InstallProgressStage,
    TextWriter,
    writeOut,
    writeOutf,
    writeOutln;

version (Posix)
{
    import core.sys.posix.unistd : isatty;
}
version (Windows)
{
    private extern (C) int _isatty(int fd) nothrow @nogc;
}

/// Braille animation frames rendered while a tool is installing.
private immutable string[] spinnerFrames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

private enum Duration installSpinnerInterval = dur!"msecs"(200);

/// Reports whether `writer` wraps a live character terminal file descriptor.
bool isTTY(TextWriter writer) @trusted
{
    auto fw = cast(FileTextWriter) writer;
    if (fw is null)
        return false;
    try
    {
        auto f = fw.streamFile();
        if (!f.isOpen)
            return false;
        version (Posix)
            return isatty(f.fileno) != 0;
        else version (Windows)
            return _isatty(f.fileno) != 0;
        else
            return false;
    }
    catch (Exception)
    {
        return false;
    }
}

/// Reports whether standard input is connected to an interactive terminal.
bool stdinIsTTY() @trusted
{
    try
    {
        if (!stdin.isOpen)
            return false;
        version (Posix)
            return isatty(stdin.fileno) != 0;
        else version (Windows)
            return _isatty(stdin.fileno) != 0;
        else
            return false;
    }
    catch (Exception)
    {
        return false;
    }
}

private struct ToolState
{
    string tool;
    string version_;
    InstallProgressStage stage;
    bool done = false;
}

/// Renders live multi-line spinner status for concurrent tool installs on TTYs
/// or plain line-by-line progress on non-TTY outputs.
final class InstallSpinner
{
    private Mutex mu;
    private Condition stopCond;
    private TextWriter out_;
    private bool tty = false;
    private ToolState[] tools;
    private size_t[string] keys;
    private string[] lines;
    private size_t frame = 0;
    private bool stopRequested = false;
    private Thread animThread;

    this(TextWriter out_, size_t requests) @trusted
    {
        this.mu = new Mutex();
        this.stopCond = new Condition(this.mu);
        this.out_ = out_;
        this.tty = isTTY(out_);
        if (requests > 0)
            this.tools.reserve(requests);
    }

    private static string toolKey(string tool, string ver) @safe pure
    {
        return tool ~ "@" ~ ver;
    }

    /// Allocates an ordered display slot before `start()`.
    void register(string tool, string ver) @safe
    {
        auto k = toolKey(tool, ver);
        if ((k in keys) is null)
        {
            keys[k] = tools.length;
            tools ~= ToolState(tool, ver);
        }
    }

    /// Emits blank placeholder lines so ANSI cursor-up redraws can rewrite rows in-place.
    void printInitialLines() @trusted
    {
        if (!tty)
            return;
        foreach (_; tools)
            writeOutln(out_, "");
    }

    /// Starts the background ANSI animation thread (TTY only).
    void start() @trusted
    {
        if (!tty)
            return;

        hideCursor();
        {
            mu.lock();
            scope (exit) mu.unlock();
            stopRequested = false;
            render();
        }

        animThread = new Thread({
            mu.lock();
            scope (exit) mu.unlock();
            while (!stopRequested)
            {
                bool notified = stopCond.wait(installSpinnerInterval);
                if (stopRequested)
                    break;
                if (!notified)
                {
                    frame = (frame + 1) % spinnerFrames.length;
                    render();
                }
            }
        });
        animThread.start();
    }

    /// Updates a tool's installation stage (safe to invoke concurrently).
    void update(string tool, string ver, InstallProgressStage stage) @trusted
    {
        auto k = toolKey(tool, ver);
        mu.lock();
        scope (exit) mu.unlock();

        auto idxPtr = k in keys;
        if (idxPtr is null)
            return;

        size_t idx = *idxPtr;
        tools[idx].stage = stage;
        if (stage == InstallProgressInstalled)
            tools[idx].done = true;

        if (tty)
        {
            render();
            return;
        }

        writeOutf(out_, "%s %s: %s\n", tools[idx].tool, tools[idx].version_, cast(string) stage);
    }

    /// Stops the animation loop and writes final completion checkmarks (TTY only).
    void stop() @trusted
    {
        if (!tty)
            return;

        {
            mu.lock();
            scope (exit) mu.unlock();
            stopRequested = true;
            stopCond.notifyAll();
        }
        if (animThread !is null)
            animThread.join();

        Thread.sleep(dur!"msecs"(20));

        mu.lock();
        scope (exit) mu.unlock();
        renderFinal();
        showCursor();
    }

    private void hideCursor()
    {
        writeOut(out_, "\033[?25l");
    }

    private void showCursor()
    {
        writeOut(out_, "\033[?25h");
    }

    private void render()
    {
        size_t n = tools.length;
        if (n == 0)
            return;
        if (lines.length != n)
            lines.length = n;

        foreach (i, t; tools)
        {
            auto line = renderLine(t);
            if (lines[i] == line)
                continue;
            replaceLine(i, line);
            lines[i] = line;
        }
    }

    private string renderLine(ToolState t) const @safe
    {
        string icon = spinnerFrames[frame];
        if (t.done)
            icon = "✓";
        else if (t.stage == InstallProgressDownloading)
            icon = "↓";
        return format("%s %s %s", icon, t.tool, t.version_);
    }

    private void replaceLine(size_t index, string line)
    {
        size_t up = tools.length - index;
        if (up > 0)
            writeOutf(out_, "\033[%dA", up);
        writeOutf(out_, "\033[2K%s\n", line);
        if (tools.length > index + 1)
        {
            size_t down = tools.length - index - 1;
            writeOutf(out_, "\033[%dB", down);
        }
    }

    private void renderFinal()
    {
        size_t n = tools.length;
        if (n == 0)
            return;
        if (lines.length != n)
            lines.length = n;

        foreach (i, t; tools)
        {
            auto line = format("✓ %s %s", t.tool, t.version_);
            if (lines[i] == line)
                continue;
            replaceLine(i, line);
            lines[i] = line;
        }
    }
}

unittest
{
    import std.algorithm : canFind;

    // Non-TTY buffer writers never treat streams as terminals and emit plain lines.
    auto buf = new BufferTextWriter();
    assert(!isTTY(buf));

    auto sp = new InstallSpinner(buf, 1);
    sp.register("php", "8.4");
    sp.printInitialLines();
    sp.start();
    sp.update("php", "8.4", InstallProgressDownloading);
    sp.update("php", "8.4", InstallProgressInstalled);
    sp.stop();

    auto outText = buf.text();
    assert(outText.canFind("php 8.4: downloading\n"));
    assert(outText.canFind("php 8.4: installed\n"));
}
