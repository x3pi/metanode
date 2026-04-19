package important_data_logger

import (
	"fmt"
	"os"
)

const LOG_FILE = "important_data.log"

func WriteImportantLog(v any) {
	// Open the log file
	file, err := os.OpenFile(LOG_FILE, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		return
	}
	defer file.Close() // Ensure the file is closed when done

	// Format the log message
	message := fmt.Sprintf("%v\n", v)

	// Write to the file
	if _, err := file.Write([]byte(message)); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing to log file: %v\n", err)
	}

	// Write to stdout
	fmt.Print(message)
}
