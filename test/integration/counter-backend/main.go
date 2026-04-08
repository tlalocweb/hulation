package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var counter int64

func main() {
	http.HandleFunc("/api/count", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(map[string]int64{"count": atomic.LoadInt64(&counter)})
		case "POST":
			val := atomic.AddInt64(&counter, 1)
			json.NewEncoder(w).Encode(map[string]int64{"count": val})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	})
	fmt.Println("counter-backend listening on :8080")
	http.ListenAndServe(":8080", nil)
}
