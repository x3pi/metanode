package utils

import (
	"fmt"
	"log"
)

func CheckErr(err error, msg string) {
	if err != nil {
		fullMsg := fmt.Sprintf("%s: %v", msg, err)
		log.Fatal(fullMsg)
	}
}
