package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// The public loopback page contains no authorization URL, state or session code.
// Only the browser receives the one-use redirect from /begin.
func handoff(ctx context.Context) (func(string) error, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, errors.New("local handoff unavailable")
	}
	var mu sync.Mutex
	address := ""
	started := false
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Suburban owner sign-in</title><h1>Suburban owner sign-in</h1><p>Sign in with the enrolled owner account. Codes and credentials remain private.</p><a href="/begin">Continue to owner sign-in</a>`)
	})
	mux.HandleFunc("/begin", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" || address == "" || started {
			w.WriteHeader(410)
			return
		}
		started = true
		http.Redirect(w, r, address, http.StatusSeeOther)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	show := func(value string) error {
		mu.Lock()
		address = value
		mu.Unlock()
		fmt.Println("Open this local page in the approved Chrome browser:")
		fmt.Println("http://" + listener.Addr().String() + "/start")
		return nil
	}
	close := func() {
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}
	return show, close, nil
}
