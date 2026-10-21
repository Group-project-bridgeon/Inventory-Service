package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeShutdown(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "force"}[force], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			exited := make(chan struct{})
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(exited)
				close(entered)
				if force {
					<-r.Context().Done()
				} else {
					<-release
					w.WriteHeader(204)
				}
			})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer server.Close()
			timeout := time.Second
			if force {
				timeout = 20 * time.Millisecond
			}
			result := make(chan error, 1)
			go func() { result <- serve(ctx, server, listener, timeout) }()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				client := &http.Client{Timeout: 2 * time.Second}
				resp, err := client.Get("http://" + listener.Addr().String())
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request not started")
			}
			cancel()
			if !force {
				select {
				case <-result:
					t.Fatal("did not drain active request")
				case <-time.After(20 * time.Millisecond):
				}
				close(release)
			}
			select {
			case err := <-result:
				if (err != nil) != force {
					t.Fatalf("shutdown error %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown hung")
			}
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("handler not canceled")
			}
			<-requestDone
		})
	}
}
