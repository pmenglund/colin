package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/pmenglund/colin/internal/app"
)

func main() {
	handler, err := app.NewServer()
	if err != nil {
		log.Fatal(err)
	}

	addr := "127.0.0.1:8080"
	if value := os.Getenv("E2E_BASE_URL"); value != "" {
		addr = strings.TrimPrefix(value, "http://")
	}

	log.Fatal(http.ListenAndServe(addr, handler))
}
