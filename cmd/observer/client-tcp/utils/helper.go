package utils

import "fmt"

func CheckErr(err error, msg string) {
	if err != nil {
		fullMsg := fmt.Sprintf("%s: %v", msg, err)
		panic(fullMsg)
	}
}
