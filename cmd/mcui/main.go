package main

import (
	"github.com/patik/mcui/internal/app"
	"log"
	"os"
)

func main() {
	root := os.Getenv("MCUI_SERVERS_DIR")
	if root == "" {
		root = "./servers"
	}
	addr := os.Getenv("MCUI_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	log.Fatal(app.Serve(addr, root))
}
