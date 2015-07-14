package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
)

var (
	listen = flag.String("l", ":8081", "port to accept requests")
)

func handler(w http.ResponseWriter, r *http.Request) {
	dump, _ := httputil.DumpRequest(r, true)
	fmt.Printf("Request: <%s>", string(dump))

	http.Error(w, "Time out", 504)
}

func main() {
	flag.Parse()

	http.HandleFunc("/", handler)
	http.ListenAndServe(*listen, nil)
}
