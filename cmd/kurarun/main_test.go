// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: Copyright contributors to the kurarun project.

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunLogsAndPrintsFailureOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, []byte("previous execution\n"), 0660); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(options{LogPath: path}, []string{"sh", "-c", "printf hello; printf error >&2; exit 7"}, &stdout, &stderr)
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"command start: sh -c", "hello", "error", "command exited with code: 7"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log does not contain %q: %s", want, data)
		}
	}
	if strings.Contains(stdout.String(), "previous execution") {
		t.Fatalf("failure stdout includes an earlier execution: %q", stdout.String())
	}
	if got := string(data); got != "previous execution\n"+stdout.String() {
		t.Fatalf("log = %q, want earlier and current execution records", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("failure stderr = %q, want empty", got)
	}
}

func TestRunDoesNotPrintSuccessfulLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	var stdout, stderr bytes.Buffer
	code := run(options{LogPath: path}, []string{"sh", "-c", "printf hello; printf error >&2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("success stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("success stderr = %q, want empty", got)
	}
}

func TestRunTruncatePrintsFailureOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, []byte("previous execution\n"), 0660); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(options{LogPath: path, Truncate: true}, []string{"sh", "-c", "printf failure; exit 7"}, &stdout, &stderr)
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "previous execution") {
		t.Fatalf("truncated log includes an earlier execution: %q", data)
	}
	if got := stdout.String(); got != string(data) {
		t.Fatalf("failure stdout = %q, want current execution log %q", got, data)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("failure stderr = %q, want empty", got)
	}
}

func TestRunNamePrefixesEveryRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	code := run(options{LogPath: path, Name: "nightly"}, []string{"sh", "-c", "printf hello; printf error >&2"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, " [nightly] ") {
			t.Errorf("record does not have name prefix: %s", line)
		}
	}
}

func TestRunNameDashUsesCommandFullPath(t *testing.T) {
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "command")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nprintf hello\n"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "job.log")
	if code := run(options{LogPath: path, Name: "-"}, []string{commandPath}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), " ["+commandPath+"] ") {
		t.Fatalf("log does not contain command full path prefix: %s", data)
	}
}

func TestRunTeeWritesLogRecordsToTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	var stdout, stderr bytes.Buffer
	code := run(options{LogPath: path, Tee: true}, []string{"sh", "-c", "printf hello; printf error >&2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "hello") || !strings.Contains(stderr.String(), "error") {
		t.Fatalf("terminal output = stdout %q, stderr %q, want command output", stdout.String(), stderr.String())
	}
}

func TestRunWithoutLogWritesOutputToTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(options{}, []string{"sh", "-c", "printf hello; printf error >&2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "hello") {
		t.Fatalf("stdout = %q, want command output", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Fatalf("stderr = %q, want command output", stderr.String())
	}
	if !strings.Contains(stdout.String(), "T") || !strings.Contains(stdout.String(), " hello\n") {
		t.Fatalf("stdout = %q, want timestamped command output", stdout.String())
	}
	if !strings.Contains(stderr.String(), "T") || !strings.Contains(stderr.String(), " error\n") {
		t.Fatalf("stderr = %q, want timestamped command output", stderr.String())
	}
}

func TestRunJSONOutputIsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.jsonl")
	code := run(options{LogPath: path, Format: "json"}, []string{"printf", "hello"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]string
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSONL record %q: %v", line, err)
		}
		if record["timestamp"] == "" || record["message"] == "" {
			t.Fatalf("incomplete record: %v", record)
		}
	}
}

func TestRunKeepsPartialWritesInOneRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	code := run(options{LogPath: path}, []string{"sh", "-c", "printf hello; sleep 0.01; printf ' world\\n'"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), " hello world\n"); count != 1 {
		t.Fatalf("combined output records = %d, want 1: %s", count, data)
	}
	if strings.Contains(string(data), " hello\n") {
		t.Fatalf("partial output was recorded separately: %s", data)
	}
}

func TestRunJSONEncodesNonUTF8OutputLosslessly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.jsonl")
	code := run(options{LogPath: path, Format: "json"}, []string{"sh", "-c", "printf '\\377\\n'"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]string
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["stream"] == "stdout" {
			if record["encoding"] != "base64" {
				t.Fatalf("encoding = %q, want base64", record["encoding"])
			}
			decoded, err := base64.StdEncoding.DecodeString(record["message"])
			if err != nil || !bytes.Equal(decoded, []byte{0xff}) {
				t.Fatalf("decoded output = %x, err = %v", decoded, err)
			}
			return
		}
	}
	t.Fatal("stdout record not found")
}

func TestForwardSignalsForwardsEverySignal(t *testing.T) {
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	forwarded := make(chan syscall.Signal, 2)
	completed := make(chan struct{})
	go func() {
		forwardSignals(signals, done, func(sig syscall.Signal) { forwarded <- sig })
		close(completed)
	}()

	signals <- syscall.SIGTERM
	signals <- syscall.SIGINT
	for _, want := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		select {
		case got := <-forwarded:
			if got != want {
				t.Fatalf("forwarded signal = %v, want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("signal %v was not forwarded", want)
		}
	}
	close(done)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("signal forwarding did not stop")
	}
}

func TestRunLogDashAppendsToCommandPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(dir, "tmp", "test1")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldDir) }()

	if code := run(options{LogPath: "-"}, []string{"tmp/test1"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp", "test1.log")); err != nil {
		t.Fatalf("derived log file: %v", err)
	}
}

func TestParseArgsAllowsMissingLog(t *testing.T) {
	_, command, code := parseArgs([]string{"--", "echo", "ok"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if got := strings.Join(command, " "); got != "echo ok" {
		t.Fatalf("command = %q, want echo ok", command)
	}
}

func TestParseArgsVersionDoesNotRequireLog(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, command, code := parseArgs([]string{"--version"}, &stdout, &stderr)
	if code != 0 || command != nil {
		t.Fatalf("version result = command %v, code %d", command, code)
	}
	if got := stdout.String(); got != "kurarun dev (none)\n" {
		t.Fatalf("version output = %q", got)
	}
}

func TestParseArgsPassesCommandFlagsThrough(t *testing.T) {
	opts, command, code := parseArgs([]string{"--log", "job.log", "--", "echo", "--child-flag"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if opts.LogPath != "job.log" {
		t.Fatalf("log path = %q, want job.log", opts.LogPath)
	}
	if got := strings.Join(command, " "); got != "echo --child-flag" {
		t.Fatalf("command = %q", command)
	}
}

func TestParseArgsSupportsShortOptions(t *testing.T) {
	opts, _, code := parseArgs([]string{"-t", "-q", "--tee", "--log", "job.log", "-n", "nightly", "--", "echo"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 || !opts.Truncate || !opts.Quiet || !opts.Tee || opts.Name != "nightly" {
		t.Fatalf("options = %+v, code = %d", opts, code)
	}
}

func TestParseArgsRejectsLogModesWithoutLog(t *testing.T) {
	_, _, code := parseArgs([]string{"--truncate", "--", "echo"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 125 {
		t.Fatalf("code = %d, want 125", code)
	}
}

func TestFormatCommandQuotesUnsafeArguments(t *testing.T) {
	got := formatCommand([]string{"echo", "path with spaces", "plain"})
	if got != "echo \"path with spaces\" plain" {
		t.Fatalf("unexpected command: %s", got)
	}
}

func TestTimestampUsesMillisecondPrecision(t *testing.T) {
	got := timestamp()
	dot := strings.LastIndex(got, ".")
	zone := strings.LastIndexAny(got, "+-")
	// UTC timestamps end in Z, which has no explicit '+' or '-' offset.
	if strings.HasSuffix(got, "Z") {
		zone = len(got) - 1
	}
	if dot < 0 || zone <= dot || len(got[dot+1:zone]) != 3 {
		t.Fatalf("timestamp = %q, want three fractional digits", got)
	}
}
