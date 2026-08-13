# copilot-token-rate

Watch GitHub Copilot output-token throughput by model.

The CLI reads the local Copilot SQLite session store and reports output tokens
per second over a trailing window. It counts only `output_tokens`; input,
cached, and reasoning tokens are intentionally excluded. Throughput uses the
full request duration as its denominator.

## Install

Install the latest release with mise:

```sh
mise use -g github:ekroon/copilot-token-rate
```

SQLite's `sqlite3` executable must be available in `PATH`.

## Usage

Print one snapshot for the default `~/.copilot/session-store.db`:

```sh
copilot-token-rate --once
```

Watch the last minute and refresh every ten seconds:

```sh
copilot-token-rate --watch
```

Customize the trailing window, refresh interval, database, or output format:

```sh
copilot-token-rate --watch --window 5m --interval 30s
copilot-token-rate --db /path/to/session-store.db --window 10m --json --once
```

Running with no mode flag prints one snapshot and exits. Use `--watch` for
repeated refreshes; the watch mode prints a short reminder that `--once`
provides a single snapshot.

## Privacy

The session store can contain local usage metadata. This tool reads it locally,
does not upload or transmit database contents, and only prints aggregate
throughput unless `--json` is selected.

## License

MIT
