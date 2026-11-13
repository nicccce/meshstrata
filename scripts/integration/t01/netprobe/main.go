// Standalone bounded, credential-free IPv4 echo diagnostic. Not a proxy.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

var output sync.Mutex

func event(kind string, fields map[string]any) {
	fields["event"], fields["at"] = kind, time.Now().UTC().Format(time.RFC3339Nano)
	output.Lock()
	defer output.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(fields)
}

func main() {
	mode := flag.String("mode", "client", "server or client")
	target := flag.String("target", "127.0.0.1", "literal IPv4")
	tcp := flag.String("tcp", "38443,8443,9443,28443,28444,28445", "TCP ports")
	udp := flag.String("udp", "38443,28443,28446", "UDP ports")
	allow := flag.String("allow", "127.0.0.1", "allowed IPv4 sources")
	duration := flag.Duration("duration", 35*time.Second, "server maximum lifetime")
	flag.Parse()
	allowed := map[string]bool{}
	for _, ip := range strings.Split(*allow, ",") {
		allowed[ip] = true
	}
	if net.ParseIP(*target).To4() == nil {
		panic("literal IPv4 required")
	}
	var wg sync.WaitGroup
	var closers []io.Closer
	for _, spec := range []struct{ network, ports string }{{"tcp4", *tcp}, {"udp4", *udp}} {
		for _, port := range strings.Split(spec.ports, ",") {
			if port == "" {
				continue
			}
			network, port := spec.network, port
			if *mode != "server" {
				wg.Add(1)
				go func() { defer wg.Done(); probe(network, net.JoinHostPort(*target, port)) }()
				continue
			}
			if network == "tcp4" {
				ln, err := net.Listen("tcp4", "0.0.0.0:"+port)
				if err != nil {
					panic(err)
				}
				closers = append(closers, ln)
				event("listen", map[string]any{"protocol": network, "local": ln.Addr().String(), "pid": os.Getpid()})
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
						if !allowed[ip] {
							event("source_denied", map[string]any{"remote": c.RemoteAddr().String()})
							c.Close()
							continue
						}
						wg.Add(1)
						go func() {
							defer wg.Done()
							defer c.Close()
							c.SetDeadline(time.Now().Add(2 * time.Second))
							b := make([]byte, 32)
							n, err := io.ReadFull(c, b)
							fields := map[string]any{"protocol": network, "local": c.LocalAddr().String(), "remote": c.RemoteAddr().String(), "received": n}
							if err == nil {
								sent, e := c.Write(b)
								fields["sent"] = sent
								if e != nil {
									fields["error"] = e.Error()
								}
							} else {
								fields["error"] = err.Error()
							}
							event("receive", fields)
						}()
					}
				}()
			} else {
				pc, err := net.ListenPacket("udp4", "0.0.0.0:"+port)
				if err != nil {
					panic(err)
				}
				closers = append(closers, pc)
				event("listen", map[string]any{"protocol": network, "local": pc.LocalAddr().String(), "pid": os.Getpid()})
				wg.Add(1)
				go func() {
					defer wg.Done()
					b := make([]byte, 128)
					for {
						n, remote, err := pc.ReadFrom(b)
						if err != nil {
							return
						}
						ip, _, _ := net.SplitHostPort(remote.String())
						if !allowed[ip] {
							event("source_denied", map[string]any{"remote": remote.String()})
							continue
						}
						sent, e := pc.WriteTo(b[:n], remote)
						f := map[string]any{"protocol": network, "local": pc.LocalAddr().String(), "remote": remote.String(), "received": n, "sent": sent}
						if e != nil {
							f["error"] = e.Error()
						}
						event("receive", f)
					}
				}()
			}
		}
	}
	if *mode == "server" {
		event("ready", map[string]any{"pid": os.Getpid()})
		timer := time.NewTimer(*duration)
		<-timer.C
		for _, c := range closers {
			c.Close()
		}
	}
	wg.Wait()
}

func probe(network, destination string) {
	for attempt := 1; attempt <= 2; attempt++ {
		start := time.Now()
		fields := map[string]any{"protocol": network, "destination": destination, "attempt": attempt, "started_at": start.UTC().Format(time.RFC3339Nano)}
		c, err := net.DialTimeout(network, destination, 2*time.Second)
		if err == nil {
			fields["local"], fields["remote"] = c.LocalAddr().String(), c.RemoteAddr().String()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			payload := make([]byte, 32)
			if _, err = rand.Read(payload); err == nil {
				_, err = c.Write(payload)
			}
			response := make([]byte, 32)
			if err == nil {
				_, err = io.ReadFull(c, response)
			}
			fields["echo_matches"] = err == nil && bytes.Equal(payload, response)
			sum := sha256.Sum256(payload)
			fields["payload_sha256"] = hex.EncodeToString(sum[:])
			c.Close()
		}
		if err != nil {
			fields["error"] = err.Error()
		}
		fields["seconds"] = time.Since(start).Seconds()
		event("probe", fields)
	}
}
