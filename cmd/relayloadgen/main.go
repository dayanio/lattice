// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command relayloadgen is a standalone Ferry relay load generator used to
// drill the relay P1 fault paths (ADR-0005 F4) against a live relay. It
// registers as a synthetic peer and floods Forward frames at a target peer,
// speaking the wire protocol directly: HTTP Upgrade on /ferry/v1/upgrade,
// one Register frame, then bare Forward frames.
//
// Two drills against a slow receiver (shape its downlink with e.g.
// `tc qdisc replace dev <veth> root tbf rate 1kbit burst 1540 latency 30s`):
//
//   - rate > 128 fps fills the 256-frame forward queue inside the 2 s write
//     deadline → ferry_frames_dropped_total climbs, then the writer deadline
//     fires (ferry_session_writer_stalls_total) and the session is closed.
//   - rate < 128 fps leaves the queue underfilled and only backpressures the
//     writer into its 2 s deadline → ferry_session_writer_stalls_total
//     climbs with zero drops.
//
// Watch the counters on the relay's /metrics and the "relay session writer
// failed, closing session" / "relay forward queue full" warnings in its log.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// frame builds one relay frame: the 12-byte little-endian header followed by
// the payload (internal/relay/relay_protocol.go is the normative layout).
func frame(seq uint16, cmd byte, toID uint32, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	binary.LittleEndian.PutUint16(buf[0:2], seq)
	binary.LittleEndian.PutUint32(buf[2:6], uint32(len(payload)))
	buf[6] = cmd
	binary.LittleEndian.PutUint32(buf[7:11], toID)
	copy(buf[12:], payload)
	return buf
}

func main() {
	addr := flag.String("addr", "127.0.0.1:6266", "relay server address (host:port)")
	token := flag.String("token", "", "relay auth token (empty for an open relay)")
	id := flag.Uint64("id", 4242424242, "sender peer id to register as")
	target := flag.Uint64("target", 0, "target peer id to flood (required)")
	rate := flag.Int("rate", 300, "frames per second (>128 fills the forward queue, <128 only backpressures the writer)")
	dur := flag.Int("dur", 20, "duration in seconds")
	size := flag.Int("size", 1200, "payload bytes per frame")
	flag.Parse()

	if *target == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "relayloadgen: -target is required")
		os.Exit(2)
	}
	if *id == 0 || *id > 0xFFFFFFFF {
		_, _ = fmt.Fprintln(os.Stderr, "relayloadgen: -id must be a nonzero uint32")
		os.Exit(2)
	}

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: dial %s: %v\n", *addr, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	// The relay rides an HTTP Upgrade before the frame protocol starts;
	// a bare Register on a fresh connection is rejected as garbage.
	req, err := http.NewRequest("GET", "/ferry/v1/upgrade", nil)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: build request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Upgrade", "relay")
	req.Header.Set("Connection", "Upgrade")
	// serverURL is host:port without a scheme, so NewRequest leaves Host
	// empty and Go's http server rejects the request; echo the address.
	req.Host = *addr
	if err = req.Write(conn); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: send upgrade: %v\n", err)
		os.Exit(1)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: read upgrade response: %v\n", err)
		os.Exit(1)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: upgrade rejected: %s\n", resp.Status)
		os.Exit(1)
	}
	if br.Buffered() > 0 {
		_, _ = fmt.Fprintln(os.Stderr, "relayloadgen: unexpected buffered bytes after upgrade")
		os.Exit(1)
	}

	if _, err := conn.Write(frame(1, 0x01, uint32(*id), []byte(*token))); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "relayloadgen: send register: %v\n", err)
		os.Exit(1)
	}

	// Drain the (optional) X25519 auth challenge before flooding; the
	// session is already usable in lenient mode without answering it.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{})
	fmt.Printf("registered as %d, drained %d challenge bytes\n", *id, n)

	payload := make([]byte, *size)
	tick := time.NewTicker(time.Second / time.Duration(*rate))
	defer tick.Stop()
	deadline := time.Now().Add(time.Duration(*dur) * time.Second)
	t0 := time.Now()
	sent := 0
	var seq uint16 = 100
	for range tick.C {
		if time.Now().After(deadline) {
			break
		}
		seq++
		if _, err := conn.Write(frame(seq, 0x02, uint32(*target), payload)); err != nil {
			fmt.Printf("write error after %v (%d sent): %v\n", time.Since(t0).Round(time.Millisecond), sent, err)
			return
		}
		sent++
	}
	el := time.Since(t0)
	fmt.Printf("sent %d frames in %v (%.0f fps avg)\n", sent, el.Round(time.Millisecond), float64(sent)/el.Seconds())
}
