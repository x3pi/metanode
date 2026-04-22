package main

import (
    "bytes"
    "fmt"
    "io/ioutil"
    "net/http"
)

func main() {
    payload := []byte(`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x55798165960a62cED34a0d86e36B1758D1303907","latest"],"id":1}`)
    resp, err := http.Post("http://127.0.0.1:4200", "application/json", bytes.NewBuffer(payload))
    if err != nil {
        fmt.Println("Error:", err)
        return
    }
    defer resp.Body.Close()
    body, _ := ioutil.ReadAll(resp.Body)
    fmt.Println(string(body))
}
