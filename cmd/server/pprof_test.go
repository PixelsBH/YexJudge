package main

import (
	"net/http"
	"testing"
	"time"
)

func TestStartPprofServerServesIndex(t *testing.T) {
	server, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	if server == nil {
		t.Fatal("startPprofServer() returned a nil server for an enabled address")
	}
	t.Cleanup(func() {
		_ = server.Close()
	})

	url := "http://" + server.Addr + "/debug/pprof/"
	deadline := time.Now().Add(time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", url, response.StatusCode, http.StatusOK)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s failed before server became ready: %v", url, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
