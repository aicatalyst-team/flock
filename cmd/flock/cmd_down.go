package main

import (
	"os"
	"syscall"
	"time"
)

func cmdDown(_ []string) {
	cfg := loadConfigOrExit()
	pid, err := readPID(cfg)
	if err != nil {
		die("no PID file at %s (is flock running?)", pidFilePath(cfg))
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		die("find process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		die("signal pid %d: %v", pid, err)
	}
	ok(os.Stdout, "sent SIGTERM to pid %d", pid)
	// give it a moment to exit cleanly
	time.Sleep(500 * time.Millisecond)
}
