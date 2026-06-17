package logger

import (
	"fmt"
	"time"
)

// Info logs messages with an INFO prefix and current timestamp to stdout.
func Info(message interface{}, a ...interface{}) {
	now := time.Now().Format("2006-01-02 15:04:05")
	if formatStr, ok := message.(string); ok && len(a) > 0 {
		fmt.Printf("[%s] [INFO] %s\n", now, fmt.Sprintf(formatStr, a...))
	} else {
		if len(a) > 0 {
			fmt.Printf("[%s] [INFO] %v %v\n", now, message, a)
		} else {
			fmt.Printf("[%s] [INFO] %v\n", now, message)
		}
	}
}
