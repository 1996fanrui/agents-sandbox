package main

import (
	"errors"
	"fmt"
)

const (
	exitCodeSuccess      = 0
	exitCodeRuntimeError = 1
	exitCodeUsageError   = 2
)

type cliError struct {
	exitCode int
	err      error
}

func (e *cliError) Error() string {
	return e.err.Error()
}

func usageErrorf(format string, args ...any) error {
	return &cliError{
		exitCode: exitCodeUsageError,
		err:      fmt.Errorf(format, args...),
	}
}

func runtimeErrorf(format string, args ...any) error {
	return &cliError{
		exitCode: exitCodeRuntimeError,
		err:      fmt.Errorf(format, args...),
	}
}

func exitCodeForError(err error) int {
	if err == nil {
		return exitCodeSuccess
	}

	var commandErr *cliError
	if errors.As(err, &commandErr) {
		return commandErr.exitCode
	}

	return exitCodeRuntimeError
}
