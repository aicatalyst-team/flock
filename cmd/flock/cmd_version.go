package main

import (
	"fmt"
	"runtime"
)

func cmdVersion(_ []string) {
	fmt.Printf("flock %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
