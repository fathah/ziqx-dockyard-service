// Disposable loopback SSH protocol fixture. It never executes an OS command.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"sync"
)

func main() {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if c.User() == "root" && string(password) == "fixture-only" {
			return nil, nil
		}
		return nil, fmt.Errorf("fixture authentication rejected")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Println(listener.Addr().String())
	for {
		tcp, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer tcp.Close()
			connection, channels, requests, err := ssh.NewServerConn(tcp, config)
			if err != nil {
				return
			}
			defer connection.Close()
			go ssh.DiscardRequests(requests)
			for incoming := range channels {
				if incoming.ChannelType() != "session" {
					incoming.Reject(ssh.UnknownChannelType, "fixture only supports sessions")
					continue
				}
				channel, requests, err := incoming.Accept()
				if err != nil {
					return
				}
				go serve(channel, requests)
			}
		}()
	}
}
func serve(channel ssh.Channel, requests <-chan *ssh.Request) {
	var mu sync.Mutex
	cols, rows := uint32(80), uint32(24)
	started := false
	for req := range requests {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term                      string
				Cols, Rows, Width, Height uint32
				Modes                     string
			}
			err := ssh.Unmarshal(req.Payload, &p)
			mu.Lock()
			cols, rows = p.Cols, p.Rows
			mu.Unlock()
			req.Reply(err == nil, nil)
		case "window-change":
			var p struct{ Cols, Rows, Width, Height uint32 }
			err := ssh.Unmarshal(req.Payload, &p)
			mu.Lock()
			cols, rows = p.Cols, p.Rows
			mu.Unlock()
			req.Reply(err == nil, nil)
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(req.Payload, &p)
			if p.Command == "docker ps --no-trunc --format '{{json .}}'" {
				req.Reply(true, nil)
				json.NewEncoder(channel).Encode(map[string]string{"ID": strings.Repeat("a", 64), "Names": "fixture-app", "Image": "fixture:local", "Status": "Up"})
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				channel.Close()
				return
			}
			if p.Command != "docker exec -it "+strings.Repeat("a", 64)+" /bin/sh" || started {
				req.Reply(false, nil)
				continue
			}
			started = true
			req.Reply(true, nil)
			go terminal(channel, &mu, &cols, &rows, true)
		case "shell":
			if started {
				req.Reply(false, nil)
				continue
			}
			started = true
			req.Reply(true, nil)
			go terminal(channel, &mu, &cols, &rows, false)
		default:
			req.Reply(false, nil)
		}
	}
}
func terminal(channel ssh.Channel, mu *sync.Mutex, cols, rows *uint32, container bool) {
	scanner := bufio.NewScanner(channel)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if index := strings.IndexAny(string(data), "\r\n"); index >= 0 {
			return index + 1, data[:index], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	fmt.Fprint(channel, "fixture$ ")
	for scanner.Scan() {
		switch scanner.Text() {
		case "marker":
			if container {
				fmt.Fprint(channel, "\r\nCONTAINER_OK\r\n")
			} else {
				fmt.Fprint(channel, "\r\nROOT_OK\r\n")
			}
		case "size":
			mu.Lock()
			fmt.Fprintf(channel, "\r\n%d %d\r\n", *rows, *cols)
			mu.Unlock()
		case "flood":
			io.WriteString(channel, strings.Repeat("x", 2*1024*1024))
		case "exit":
			channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			channel.Close()
			return
		default:
			fmt.Fprint(channel, "\r\nfixture: no commands executed\r\n")
		}
	}
}
