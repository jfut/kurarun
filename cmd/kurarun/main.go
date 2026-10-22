// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: Copyright contributors to the kurarun project.

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/kong"
)

// Build metadata fields are injected by linker flags at build time.
var (
	version = "dev"
	commit  = "none"
)

type options struct {
	LogPath  string           `name:"log" short:"l" placeholder:"FILE" help:"Log file path. Use '-' to append .log to the command path."`
	Name     string           `name:"name" short:"n" placeholder:"NAME" help:"Prefix log records with NAME. Use '-' for the command's full path."`
	Format   string           `name:"output" short:"o" enum:"text,json,csv" default:"text" help:"Output format (text, json, or csv)."`
	Truncate bool             `short:"t" help:"Truncate the log before execution."`
	Tee      bool             `help:"Also display log records in the terminal while the command runs."`
	Quiet    bool             `short:"q" help:"Do not display runner messages live in the terminal."`
	NoID     bool             `name:"no-id" help:"Do not include the execution ID in log records."`
	Version  kong.VersionFlag `help:"Print version information and quit."`
	Command  []string         `arg:"" optional:"" passthrough:"" placeholder:"command" help:"Command and arguments to execute."`
}

func main() {
	opts, command, code := parseArgs(os.Args[1:], os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
	if command == nil { // --help and --version complete without running a command.
		return
	}
	os.Exit(run(opts, command, os.Stdout, os.Stderr))
}

func parseArgs(args []string, stdout, stderr io.Writer) (options, []string, int) {
	var opts options
	parser, err := kong.New(&opts,
		kong.Name("kurarun"),
		kong.Description("Execute one command and record its output in a log."),
		kong.Vars{"version": fmt.Sprintf("kurarun %s (%s)", version, commit)},
		kong.Writers(stdout, stderr),
		kong.Help(printHelp),
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "kurarun: cannot configure argument parser: %v\n", err)
		return opts, nil, 125
	}

	// Let parseArgs return help and version exit statuses instead of ending the process.
	exitCode := -1
	parser.Exit = func(code int) { exitCode = code }
	if _, err := parser.Parse(args); err != nil {
		if exitCode == 0 {
			return opts, nil, 0
		}
		parser.Errorf("%s", err)
		return opts, nil, 125
	}
	if exitCode == 0 {
		return opts, nil, 0
	}
	command := opts.Command
	if len(command) > 0 && command[0] == "--" {
		// Kong preserves the delimiter for passthrough arguments; it is not a child argument.
		command = command[1:]
	}
	if len(command) == 0 {
		parser.Errorf("a command is required")
		return opts, nil, 125
	}
	if opts.LogPath == "" && opts.Truncate {
		parser.Errorf("--truncate requires --log")
		return opts, nil, 125
	}
	return opts, command, 0
}

func printHelp(options kong.HelpOptions, ctx *kong.Context) error {
	var help bytes.Buffer
	original := ctx.Stdout
	ctx.Stdout = &help
	err := kong.DefaultHelpPrinter(options, ctx)
	ctx.Stdout = original
	text := strings.Replace(help.String(), "Usage: kurarun [<command> ...] [flags]", "Usage: kurarun [flags] -- <command> ...", 1)
	_, _ = io.WriteString(original, text)
	return err
}

func run(opts options, argv []string, stdout, stderr io.Writer) int {
	executionID := ""
	if !opts.NoID {
		var err error
		executionID, err = newExecutionID()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "kurarun: cannot generate execution ID: %v\n", err)
			return 125
		}
	}
	var log io.Writer
	var logFile *os.File
	var failureLog *os.File
	logPath := opts.LogPath
	if logPath == "-" {
		logPath = argv[0] + ".log"
	}
	if logPath != "" {
		var err error
		logFile, err = openLogFile(logPath, opts.Truncate)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "kurarun: cannot open log file: %v\n", err)
			return 125
		}
		defer func() {
			if logFile != nil {
				_ = logFile.Close()
			}
		}()
	}

	// Keep a private copy for failure reporting even when no persistent log was requested.
	failureLogDir, failureLogPattern := "", "kurarun-"
	if logPath != "" {
		failureLogDir, failureLogPattern = filepath.Dir(logPath), filepath.Base(logPath)+"."
	}
	var err error
	failureLog, err = os.CreateTemp(failureLogDir, failureLogPattern)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "kurarun: cannot create failure log: %v\n", err)
		return 125
	}
	defer func() {
		_ = failureLog.Close()
		_ = os.Remove(failureLog.Name())
	}()
	if logFile == nil {
		log = failureLog
	} else {
		// Keep this execution's records separate from the shared log file.
		log = &fanoutWriter{sinks: []io.Writer{logFile, failureLog}, failed: make([]bool, 2)}
	}

	writeTerminal := opts.Tee
	terminalStdout, terminalStderr := io.Writer(io.Discard), io.Writer(io.Discard)
	if writeTerminal {
		// --tee explicitly opts into seeing records while the command is running.
		terminalStdout, terminalStderr = stdout, stderr
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := newLineLogger(log, terminalStdout, terminalStderr, opts.Format, executionID, recordPrefix(opts.Name, cmd.Path))
	start := time.Now()
	output.info(fmt.Sprintf("command start: %s", formatCommand(argv)), !opts.Quiet && writeTerminal)
	if logErr, _ := output.writeErrors(); logErr != nil {
		_, _ = fmt.Fprintf(stderr, "kurarun: cannot write execution log: %v\n", logErr)
		printFailureLog(125, failureLog, stdout)
		return 125
	}
	commandStdout := output.stdout()
	commandStderr := output.stderr()
	cmd.Stdout = commandStdout
	cmd.Stderr = commandStderr

	// Register before starting the command so an early termination signal is queued.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	if err := cmd.Start(); err != nil {
		signal.Stop(signals)
		code := startErrorCode(err)
		output.info(fmt.Sprintf("command could not start: %v", err), !opts.Quiet && writeTerminal)
		output.info(exitLine(code, time.Since(start), ""), !opts.Quiet && writeTerminal)
		closeErr := error(nil)
		if logFile != nil {
			closeErr = logFile.Close()
			logFile = nil
		}
		logErr, terminalErr := output.writeErrors()
		if logErr == nil {
			logErr = closeErr
		}
		if logErr != nil {
			_, _ = fmt.Fprintf(stderr, "kurarun: cannot write execution log: %v\n", logErr)
		} else if terminalErr != nil {
			_, _ = fmt.Fprintf(stderr, "kurarun: cannot write tee output: %v\n", terminalErr)
		}
		printFailureLog(code, failureLog, stdout)
		return code
	}

	done := make(chan struct{})
	var signalWG sync.WaitGroup
	signalWG.Add(1)
	go func() {
		defer signalWG.Done()
		forwardSignals(signals, done, func(sig syscall.Signal) {
			// Send every received signal to the child process group so shell-created children also stop.
			_ = syscall.Kill(-cmd.Process.Pid, sig)
		})
	}()
	err = cmd.Wait()
	signal.Stop(signals)
	close(done)
	signalWG.Wait()
	commandStdout.flush()
	commandStderr.flush()

	code, signalName := exitCode(err)
	output.info(exitLine(code, time.Since(start), signalName), !opts.Quiet && writeTerminal)
	closeErr := error(nil)
	if logFile != nil {
		closeErr = logFile.Close()
		logFile = nil
	}
	logErr, terminalErr := output.writeErrors()
	if logErr == nil {
		logErr = closeErr
	}
	if logErr != nil {
		_, _ = fmt.Fprintf(stderr, "kurarun: cannot write execution log: %v\n", logErr)
		if code == 0 {
			code = 125
		}
	}
	if terminalErr != nil {
		_, _ = fmt.Fprintf(stderr, "kurarun: cannot write tee output: %v\n", terminalErr)
	}
	printFailureLog(code, failureLog, stdout)
	return code
}

func openLogFile(path string, truncate bool) (*os.File, error) {
	// Open without following symlinks or blocking on special files before checking the target.
	flags := syscall.O_WRONLY | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if !truncate {
		flags |= syscall.O_APPEND
	}
	file, err := os.OpenFile(path, flags, 0660)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("log path is not a regular file")
	}
	if truncate {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

func printFailureLog(code int, failureLog *os.File, stdout io.Writer) {
	if code != 0 && failureLog != nil {
		// Print only this run's log records after a failure, so cron can notify with the details.
		if _, err := failureLog.Seek(0, io.SeekStart); err == nil {
			_, _ = io.Copy(stdout, failureLog)
		}
	}
}

func forwardSignals(signals <-chan os.Signal, done <-chan struct{}, forward func(syscall.Signal)) {
	for {
		select {
		case sig := <-signals:
			select {
			case <-done:
				return
			default:
			}
			forward(sig.(syscall.Signal))
		case <-done:
			return
		}
	}
}

func startErrorCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return 127
	}
	// A command that was found but cannot be executed follows the shell's 126 convention.
	return 126
}

func exitCode(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			return 125, ""
		}
		if status.Signaled() {
			return 128 + int(status.Signal()), signalLabel(status.Signal())
		}
		return status.ExitStatus(), ""
	}
	return 125, ""
}

func signalLabel(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	default:
		return sig.String()
	}
}

func exitLine(code int, elapsed time.Duration, signalName string) string {
	line := fmt.Sprintf("command exited with code: %d, duration: %s", code, formatDuration(elapsed))
	if signalName != "" {
		line += fmt.Sprintf(", signal: %s, status: terminated", signalName)
	}
	return line
}

func formatDuration(d time.Duration) string {
	ms := d.Milliseconds()
	day := ms / (24 * 60 * 60 * 1000)
	ms %= 24 * 60 * 60 * 1000
	hour := ms / (60 * 60 * 1000)
	ms %= 60 * 60 * 1000
	minute := ms / (60 * 1000)
	ms %= 60 * 1000
	second := ms / 1000
	ms %= 1000
	return fmt.Sprintf("0000-00-%02d %02d:%02d:%02d.%03d", day, hour, minute, second, ms)
}

func formatCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"\\$&;|<>*?()[]{}!") || containsNonPrintable(arg) {
			quoted[i] = fmt.Sprintf("%q", arg)
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}

func recordPrefix(name, commandPath string) string {
	if name == "" {
		return ""
	}
	if name == "-" {
		// cmd.Path may be relative when the command was supplied with a relative path.
		if absolutePath, err := filepath.Abs(commandPath); err == nil {
			name = absolutePath
		} else {
			name = commandPath
		}
	}
	if containsNonPrintable(name) {
		name = fmt.Sprintf("%q", name)
	}
	return "[" + name + "] "
}

func containsNonPrintable(value string) bool {
	// Escape metadata controls so command and name text cannot create fake log records.
	if !utf8.ValidString(value) {
		return true
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return true
		}
	}
	return false
}

func newExecutionID() (string, error) {
	// Keep a short random ID so records from concurrent executions can be grouped.
	var randomBytes [4]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", randomBytes[:])[:7], nil
}

// Bound per-stream buffering when a child emits output without line delimiters.
const maxRecordBytes = 64 * 1024

type lineLogger struct {
	mu            sync.Mutex
	log, out, err io.Writer
	format        string
	executionID   string
	prefix        string
	logErr        error
	terminalErr   error
}

func newLineLogger(log, out, err io.Writer, format, executionID, prefix string) *lineLogger {
	return &lineLogger{log: log, out: out, err: err, format: format, executionID: executionID, prefix: prefix}
}

func (l *lineLogger) stdout() *streamWriter {
	return &streamWriter{logger: l, terminal: l.out, stream: "stdout"}
}

func (l *lineLogger) stderr() *streamWriter {
	return &streamWriter{logger: l, terminal: l.err, stream: "stderr"}
}

func (l *lineLogger) info(message string, terminal bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var terminalWriter io.Writer
	if terminal {
		terminalWriter = l.out
	}
	l.writeRecord(timestamp(), message, "", terminalWriter)
}

func (l *lineLogger) writeRecord(ts, message, stream string, terminal io.Writer) {
	message = l.prefix + message
	var line []byte
	switch l.format {
	case "json":
		encoding, encodedMessage := encodeMessage(message)
		encoded, err := json.Marshal(jsonLogRecord{
			Timestamp: ts,
			ID:        l.executionID,
			Message:   encodedMessage,
			Stream:    stream,
			Encoding:  encoding,
		})
		if err != nil {
			if l.logErr == nil {
				l.logErr = err
			}
			return
		}
		line = append(encoded, '\n')
	case "csv":
		// Keep a fixed column order so append-only logs remain easy to process:
		// timestamp, id, message, stream, encoding. CSV quoting handles commas and quotes.
		encoding, encodedMessage := encodeMessage(message)
		var encoded bytes.Buffer
		writer := csv.NewWriter(&encoded)
		if err := writer.Write([]string{ts, l.executionID, encodedMessage, stream, encoding}); err != nil {
			if l.logErr == nil {
				l.logErr = err
			}
			return
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			if l.logErr == nil {
				l.logErr = err
			}
			return
		}
		line = encoded.Bytes()
	default:
		line = make([]byte, 0, len(ts)+len(message)+len(l.executionID)+3)
		line = append(line, ts...)
		line = append(line, ' ')
		line = append(line, l.textIDPrefix()...)
		line = append(line, message...)
		line = append(line, '\n')
	}
	if n, err := l.log.Write(line); l.logErr == nil {
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		l.logErr = err
	}
	if terminal != nil {
		if n, err := terminal.Write(line); l.terminalErr == nil {
			if err == nil && n != len(line) {
				err = io.ErrShortWrite
			}
			l.terminalErr = err
		}
	}
}

type jsonLogRecord struct {
	Timestamp string `json:"timestamp"`
	ID        string `json:"id,omitempty"`
	Message   string `json:"message"`
	Stream    string `json:"stream,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
}

func encodeMessage(message string) (string, string) {
	if !utf8.ValidString(message) {
		// JSON and CSV cannot safely represent arbitrary invalid UTF-8 text as-is.
		return "base64", base64.StdEncoding.EncodeToString([]byte(message))
	}
	return "", message
}

func (l *lineLogger) writeErrors() (error, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.logErr, l.terminalErr
}

// fanoutWriter writes each execution record to independent persistent and failure-log sinks.
type fanoutWriter struct {
	sinks  []io.Writer
	failed []bool
	err    error
}

func (w *fanoutWriter) Write(p []byte) (int, error) {
	for i, sink := range w.sinks {
		if w.failed[i] {
			continue
		}
		n, err := sink.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.failed[i] = true
			if w.err == nil {
				w.err = err
			}
		}
	}
	if w.err != nil {
		// Continue reporting full writes so a failed sink does not stop the other sinks.
		return len(p), w.err
	}
	return len(p), nil
}

func (l *lineLogger) textIDPrefix() string {
	if l.executionID == "" {
		return ""
	}
	return l.executionID + " "
}

func timestamp() string {
	// Millisecond precision is sufficient for command output ordering and keeps logs concise.
	return time.Now().Format("2006-01-02T15:04:05.000Z07:00")
}

type streamWriter struct {
	logger   *lineLogger
	terminal io.Writer
	stream   string
	pending  []byte
	skipLF   bool
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.logger.mu.Lock()
	defer w.logger.mu.Unlock()
	written := len(p)
	for {
		if w.skipLF {
			if len(p) == 0 {
				return written, nil
			}
			if len(p) > 0 && p[0] == '\n' {
				p = p[1:]
			}
			w.skipLF = false
		}
		end := bytes.IndexAny(p, "\r\n")
		if end < 0 {
			w.appendRecordData(p)
			return written, nil
		}
		w.appendRecordData(p[:end])
		delimiter := p[end]
		if len(w.pending) > 0 || delimiter == '\n' {
			// Treat CR as a record delimiter for progress meters that redraw with '\r'.
			w.logger.writeRecord(timestamp(), string(w.pending), w.stream, w.terminal)
			w.pending = w.pending[:0]
		}
		if delimiter == '\r' {
			// Ignore the LF in a CRLF pair, including when the pair crosses writes.
			w.skipLF = true
		}
		p = p[end+1:]
	}
}

func (w *streamWriter) appendRecordData(data []byte) {
	for len(data) > 0 {
		room := maxRecordBytes - len(w.pending)
		take := min(room, len(data))
		w.pending = append(w.pending, data[:take]...)
		data = data[take:]
		if len(w.pending) == maxRecordBytes {
			w.logger.writeRecord(timestamp(), string(w.pending), w.stream, w.terminal)
			w.pending = w.pending[:0]
		}
	}
}

func (w *streamWriter) flush() {
	w.logger.mu.Lock()
	defer w.logger.mu.Unlock()
	if len(w.pending) == 0 {
		return
	}
	w.logger.writeRecord(timestamp(), string(w.pending), w.stream, w.terminal)
	w.pending = nil
}
