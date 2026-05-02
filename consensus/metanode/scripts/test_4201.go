package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:4201", 5*time.Second)
	if err != nil {
		fmt.Printf("Failed to connect to 4201: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Println("Successfully connected to 4201!")
}
