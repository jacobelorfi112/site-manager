//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

func main() {
	body := `{"cards":["4165987618898226|10|26|275|https://apex-strength-co-5.myshopify.com"]}`
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Post("http://localhost:8082/check", "application/json", bytes.NewBufferString(body))
	if err != nil {
		fmt.Println("ERROR:", err)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %d\nBody: %s\n", resp.StatusCode, string(data))
}
