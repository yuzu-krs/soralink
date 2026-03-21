package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := "3000"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Soralink Test</title></head>
<body>
<h1>Soralink is working!</h1>
<p>Time: %s</p>
<p>Remote: %s</p>
<p>Host: %s</p>
</body>
</html>`, time.Now().Format(time.RFC3339), r.RemoteAddr, r.Host)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","time":"%s"}`, time.Now().Format(time.RFC3339))
	})

	log.Printf("test server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
