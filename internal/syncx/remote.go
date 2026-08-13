package syncx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const maxRemoteBundleSize = 6 << 20

type RemoteOptions struct {
	Host           string
	User           string
	Binary         string
	SourceUserData string
	ConnectTimeout time.Duration
	ExportTimeout  time.Duration
}

func FetchBundle(ctx context.Context, options RemoteOptions) (Bundle, error) {
	if err := validateRemoteOptions(options); err != nil {
		return Bundle{}, err
	}
	inner := "exec " + shellQuote(options.Binary)
	if options.SourceUserData != "" {
		inner += " --user-data " + shellQuote(options.SourceUserData)
	}
	inner += " export"
	remoteCommand := `exec "$SHELL" -lc ` + shellQuote(inner)
	timeoutContext, cancel := context.WithTimeout(ctx, options.ExportTimeout)
	defer cancel()
	command := exec.CommandContext(timeoutContext, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout="+strconv.Itoa(int(options.ConnectTimeout/time.Second)),
		"--", options.User+"@"+options.Host, remoteCommand,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Bundle{}, fmt.Errorf("prepare remote export: %w", err)
	}
	stderr := cappedBuffer{maximum: 64 << 10}
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return Bundle{}, fmt.Errorf("start remote export: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxRemoteBundleSize+1))
	if len(output) > maxRemoteBundleSize {
		_ = command.Process.Kill()
		_ = command.Wait()
		return Bundle{}, fmt.Errorf("remote bundle exceeds 6 MiB")
	}
	waitErr := command.Wait()
	if timeoutContext.Err() != nil {
		return Bundle{}, fmt.Errorf("remote export timed out after %s", options.ExportTimeout)
	}
	if readErr != nil {
		return Bundle{}, fmt.Errorf("read remote export: %w", readErr)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			message := strings.TrimSpace(stderr.String())
			if message != "" {
				return Bundle{}, fmt.Errorf("remote export: %s", message)
			}
		}
		return Bundle{}, fmt.Errorf("remote export: %w", waitErr)
	}
	bundle, err := DecodeBundle(output)
	if err != nil {
		return Bundle{}, fmt.Errorf("remote export returned an invalid bundle: %w", err)
	}
	return bundle, nil
}

func validateRemoteOptions(options RemoteOptions) error {
	if options.Host == "" || strings.HasPrefix(options.Host, "-") || strings.ContainsAny(options.Host, "@ \t\r\n\x00") {
		return fmt.Errorf("SSH host is invalid")
	}
	if options.User == "" || strings.HasPrefix(options.User, "-") || strings.ContainsAny(options.User, "@ \t\r\n\x00") {
		return fmt.Errorf("SSH user is invalid")
	}
	if options.Binary == "" || strings.ContainsAny(options.Binary, "\r\n\x00") {
		return fmt.Errorf("source binary is invalid")
	}
	if options.ConnectTimeout < time.Second || options.ConnectTimeout > time.Hour || options.ConnectTimeout%time.Second != 0 {
		return fmt.Errorf("SSH connect timeout must be whole seconds from 1s to 1h")
	}
	if options.ExportTimeout < time.Second || options.ExportTimeout > time.Hour || options.ExportTimeout%time.Second != 0 {
		return fmt.Errorf("export timeout must be whole seconds from 1s to 1h")
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type cappedBuffer struct {
	bytes.Buffer
	maximum int
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.maximum - buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.Buffer.Write(data)
	}
	return written, nil
}
