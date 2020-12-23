package internal

import (
	"fmt"
)

var (
	Verbose = false
)

func LogInfo(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

func LogError(format string, a ...interface{}) {
	fmt.Printf("err: "+format+"\n", a...)
}

func LogDebug(format string, a ...interface{}) {
	if Verbose {
		fmt.Printf("debug: "+format+"\n", a...)
	}
}
