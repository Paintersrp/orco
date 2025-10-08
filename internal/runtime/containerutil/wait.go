package containerutil

import (
	"errors"
	"fmt"
	"strings"
)

type WaitStatus struct {
	ExitCode     int64
	ErrorMessage string
	Err          error
	OOMKilled    bool
	MemoryLimit  string
}

func WaitReadyError(status WaitStatus) error {
	if status.Err != nil {
		return wrapOOMError(status.Err, status)
	}
	if status.ErrorMessage != "" {
		if status.ExitCode != 0 {
			return wrapOOMError(fmt.Errorf("container exited with status %d: %s", status.ExitCode, status.ErrorMessage), status)
		}
		return wrapOOMError(errors.New(status.ErrorMessage), status)
	}
	if status.ExitCode != 0 {
		return wrapOOMError(fmt.Errorf("container exited with status %d", status.ExitCode), status)
	}
	return wrapOOMError(errors.New("container exited before ready"), status)
}

func WaitExitError(status WaitStatus) error {
	if status.Err != nil {
		return wrapOOMError(status.Err, status)
	}
	if status.ErrorMessage != "" {
		if status.ExitCode != 0 {
			return wrapOOMError(fmt.Errorf("container exited with status %d: %s", status.ExitCode, status.ErrorMessage), status)
		}
		return wrapOOMError(errors.New(status.ErrorMessage), status)
	}
	if status.ExitCode != 0 {
		return wrapOOMError(fmt.Errorf("container exited with status %d", status.ExitCode), status)
	}
	return nil
}

func wrapOOMError(err error, status WaitStatus) error {
	if err == nil {
		return nil
	}
	if !status.OOMKilled {
		return err
	}
	limit := strings.TrimSpace(status.MemoryLimit)
	if limit != "" {
		return fmt.Errorf("container terminated by the kernel OOM killer (memory limit %s): %w", limit, err)
	}
	return fmt.Errorf("container terminated by the kernel OOM killer: %w", err)
}
