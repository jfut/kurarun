# kurarun

[![Tag](https://img.shields.io/github/tag/jfut/kurarun.svg)](https://github.com/jfut/kurarun/releases)
[![License](https://img.shields.io/badge/license-Apache%202-blue)](https://github.com/jfut/kurarun/blob/main/LICENSE)

`kurarun` is a Linux CLI tool that runs a command and can record its output and exit status to a log file. It is intended for use with cron, systemd timers, CI/CD, and scheduled batch jobs.

It can be used as a modern alternative to [cronlog](https://github.com/kazuho/kaztools/blob/master/cronlog) when you need timestamped command output and exit-status logging for scheduled jobs.

## Installation

Download an RPM package from the [Releases](https://github.com/jfut/kurarun/releases) page, or configure the RPM repository below on RHEL-compatible distributions.

### Install with dnf-plugin-anyrepo

First, install [dnf-plugin-anyrepo](https://github.com/jfut/dnf-plugin-anyrepo) by following its installation instructions.

Next, import the RPM public signing key, add the repository, and install the package with `dnf`.

```bash
rpm --import https://raw.githubusercontent.com/jfut/kurarun/refs/heads/main/RPM-GPG-KEY-jfut-github
dnf-anyrepo add https://github.com/jfut/kurarun
dnf install kurarun
```

To upgrade the package later, run:

```bash
dnf upgrade kurarun
```

## Usage

Run a command without writing a log file:

```bash
kurarun -- echo "run without a log file"
```

Append the execution log to a file in the current directory:

```bash
kurarun -l job.log -- rsync -a /src/ /dst/
```

Write the execution log to an explicitly specified path:

```bash
kurarun -l /var/log/backup.log -- /home/backup/bin/backup
```

Derive the log path by appending `.log` to the command path (`/home/backup/bin/backup.log`):

```bash
kurarun -l - -- /home/backup/bin/backup
```

Write the execution log as JSONL:

```bash
kurarun --log job.json -o json -- /home/backup/bin/backup
```

Write the execution log and also display it in the terminal:

```bash
kurarun -l job.log --tee -- /home/backup/bin/backup
```

Prefix every log record with a job name:

```bash
kurarun -n backup -l job.log -- /home/backup/bin/backup
```

Use the command's full path as the prefix:

```bash
kurarun -n - -l job.log -- /home/backup/bin/backup
```

Everything after `--` is the command to execute and its arguments. A shell is not used implicitly. Specify `sh -c '...'` explicitly only when shell syntax is needed.

### Options

- `-l`, `--log FILE`: Write execution logs to FILE. Use `-l -` to append `.log` to the command path (for example, `tmp/backup.sh.log`)
- `-n`, `--name NAME`: Prefix every record with `[NAME]`. Use `-n -` to use the command's full path as the name
- `-o`, `--output FORMAT`: Select the output format: `text` (default) or `json`
- `-t`, `--truncate`: Empty the log before execution
- `--tee`: Also write log records to the terminal
- `-q`, `--quiet`: Do not display kurarun's start and exit lines in the terminal (they are still written to the log)
- `--version`: Display the version
- `-h`, `--help`: Display usage information

## Log format

Each line begins with an RFC 3339 timestamp with millisecond precision and a time zone. The start line, the child process's standard output and standard error, and the exit line are written to the same file in real time. The arrival order of standard output and standard error is preserved as much as possible.

Without `--log`, timestamped child process output is displayed in the terminal and no log file is written. With `--log`, output is written only to the log file unless `--tee` is specified. `--truncate` requires `--log`.

If the command exits with a non-zero status, kurarun writes this execution's complete log records to standard output after the command finishes, so cron can send them by email. This output contains only the failed execution's records even when multiple kurarun processes append to the same log file concurrently.

```text
2026-07-11T00:42:06.639+09:00 command start: /usr/local/bin/backup.sh
2026-07-11T00:42:07.112+09:00 backup started
2026-07-11T00:43:20.220+09:00 command exited with code: 0, duration: 0000-00-00 00:01:14.108
```

With `-n backup`, each record has the name after its timestamp:

```text
2026-07-11T00:42:06.639+09:00 [backup] command start: /usr/local/bin/backup.sh
2026-07-11T00:42:07.112+09:00 [backup] backup started
2026-07-11T00:43:20.220+09:00 [backup] command exited with code: 0, duration: 0000-00-00 00:01:14.108
```

With `-o json`, each record is written as one JSON object per line (JSONL):

```json
{"timestamp":"2026-07-11T00:42:06.639+09:00","message":"command start: /usr/local/bin/backup.sh"}
{"timestamp":"2026-07-11T00:42:07.112+09:00","message":"backup started","stream":"stdout"}
```

When a child process emits non-UTF-8 bytes, the record uses `"encoding":"base64"` and stores the original bytes as Base64 in `message`. This preserves arbitrary command output without producing invalid JSON.

If the log file does not exist, it is created with mode `0660` (before applying the umask). Parent directories are not created. Command-line arguments may contain sensitive information, so take care with both log-file permissions and argument contents.

## Exit codes and signals

The child process's exit code is returned unchanged. Failure to start returns `126` (not executable) or `127` (not found); kurarun's own initialization errors return `125`. Upon receiving `SIGINT`, `SIGTERM`, `SIGHUP`, or `SIGQUIT`, kurarun forwards the signal to the child process group. Signal termination is logged and returned as `128 + signal`.

The log file is opened and closed for each execution, so kurarun can be used with standard logrotate configurations.

## Development

```bash
just update
just lint
just test
just build
just snapshot
```

## Release packaging with goreleaser

Build release artifacts locally:

```bash
just release
```

## Release

GitHub Actions signs RPM artifacts with the GPG private key stored in `RPM_SIGNING_KEY`. If the key has a passphrase, store it in `NFPM_PASSPHRASE`.

1. Run `git tag -s vX.Y.Z -m vX.Y.Z`.
2. Run `git push origin vX.Y.Z` and wait for the Release to be created.
3. Edit the created Release.
4. Press the `Generate release notes` button and edit the release notes.
5. Press the `Update release` button.

## License

Apache-2.0

Copyright contributors to the kurarun project.

## Author

Jun Futagawa (jfut)
