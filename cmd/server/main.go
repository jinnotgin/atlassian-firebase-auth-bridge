package main

import (
	"log"
	"net/http"
	"os"

	"github.com/jinnotgin/atlassian-firebase-auth-bridge/internal/authbridge"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", authbridge.Handler)

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}