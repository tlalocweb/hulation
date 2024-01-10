package main

import (
	"net/http"
	"time"
)

func hello(w http.ResponseWriter, req *http.Request) {
	debugf("Got /hello\n")
	w.WriteHeader(200)
	w.Write([]byte("hello"))
}

func main() {
	go func() {
		http.HandleFunc("/hello", hello)
		http.ListenAndServe(":8080", nil)
	}()

	debugf("Hello world. Start.\n")
	for i := 0; true; i++ {
		debugf("Hello world. %d\n", i)
		time.Sleep(5 * time.Second)
	}
}
