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
	silent   bool
}

func (e *cliError) Error() string {
	if e.err == nil {
		return ""
	}
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

func exitCodeError(exitCode int) error {
	return &cliError{
		exitCode: exitCode,
		silent:   true,
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

func shouldPrintError(err error) bool {
	var commandErr *cliError
	if errors.As(err, &commandErr) {
		return !commandErr.silent && commandErr.err != nil
	}
	return true
}
