module app;

// Binary entry point delegating command execution to `polka.cli.run`.

import std.stdio : stderr, stdout;

import polka.backend : FileTextWriter;
import polka.cli : run;

version (unittest)
{
    void main() {}
}
else
{
    int main(string[] args)
    {
        auto outWriter = new FileTextWriter(stdout);
        auto errWriter = new FileTextWriter(stderr);
        const(string)[] cliArgs = args.length > 1 ? args[1 .. $] : null;
        return run(outWriter, errWriter, cliArgs);
    }
}
