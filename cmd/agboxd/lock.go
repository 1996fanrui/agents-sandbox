package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type hostLock struct {
	file *os.File
	path string
}

func acquireHostLock(lockPath string) (*hostLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory for %s: %w", lockPath, err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open host lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("agboxd host lock %s is already held; refuse to start a second daemon on this machine", lockPath)
		}
		return nil, fmt.Errorf("acquire host lock %s: %w", lockPath, err)
	}
	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("truncate host lock %s: %w", lockPath, err)
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("write host lock %s: %w", lockPath, err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("rewind host lock %s: %w", lockPath, err)
	}
	return &hostLock{file: file, path: lockPath}, nil
}

func (lockHandle *hostLock) release() error {
	if lockHandle == nil || lockHandle.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lockHandle.file.Fd()), syscall.LOCK_UN)
	closeErr := lockHandle.file.Close()
	lockHandle.file = nil
	if unlockErr != nil {
		return fmt.Errorf("release host lock %s: %w", lockHandle.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close host lock %s: %w", lockHandle.path, closeErr)
	}
	return nil
}
