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

// Command tvoslib is not a real command: the package name must be main
// because -buildmode=c-archive requires exactly one main package (verified
// 2026-09-30, see task-2-report.md §5), while the directory — and the
// engine package Task 4 links against — stays tvoslib. It exports the
// embedded Lattice engine over a plain C ABI for tvOS apps that link the
// static library directly (no gomobile). See
// docs/superpowers/specs/2026-09-30-tvos-cast-design.md §4.2.
package main

/*
#include <stdlib.h>
typedef void (*TVEventFn)(const char* event, void* ctx);
static void tvoslibDispatch(TVEventFn fn, void* ctx, const char* event) {
	if (fn != 0) { fn(event, ctx); }
}
*/
import "C"

import (
	"context"
	"net"
	"sync"
	"unsafe"

	"github.com/alatticeio/lattice/apple/engine/embedded"
)

var (
	mu     sync.Mutex
	engine *embedded.EmbeddedEngine
)

func setEngine(e *embedded.EmbeddedEngine) { mu.Lock(); engine = e; mu.Unlock() }
func getEngine() *embedded.EmbeddedEngine  { mu.Lock(); defer mu.Unlock(); return engine }

// handles is the Go-side GC root for open media handles. Once TVOpenURL has
// written a handle pointer into C memory, Go no longer tracks it; without an
// explicit root the collector could free the handle while C still uses it
// (use-after-free). TVOpenURL registers on success, TVClose unregisters
// before closing. The C ABI is unchanged.
var handles = struct {
	sync.Mutex
	m map[unsafe.Pointer]*mediaHandle
}{m: map[unsafe.Pointer]*mediaHandle{}}

func registerHandle(h *mediaHandle) unsafe.Pointer {
	p := unsafe.Pointer(h)
	handles.Lock()
	handles.m[p] = h
	handles.Unlock()
	return p
}

func unregisterHandle(p unsafe.Pointer) {
	handles.Lock()
	delete(handles.m, p)
	handles.Unlock()
}

//export TVStart
func TVStart(cfg *C.char, onEvent C.TVEventFn, ctx unsafe.Pointer) *C.char {
	e, err := embedded.New(C.GoString(cfg))
	if err != nil {
		return C.CString("config: " + err.Error())
	}
	if onEvent != nil {
		e.SetEventHandler(func(event string) {
			cEvent := C.CString(event)
			defer C.free(unsafe.Pointer(cEvent))
			C.tvoslibDispatch(onEvent, ctx, cEvent)
		})
	}
	if err := e.StartAsync(); err != nil {
		return C.CString("start: " + err.Error())
	}
	setEngine(e)
	return nil
}

//export TVStop
func TVStop() {
	if e := getEngine(); e != nil {
		_ = e.Stop()
	}
	setEngine(nil)
}

//export TVOverlayAddress
func TVOverlayAddress() *C.char {
	e := getEngine()
	if e == nil {
		return C.CString("")
	}
	return C.CString(e.OverlayAddress())
}

//export TVPrivateKey
func TVPrivateKey() *C.char {
	// The embedded engine generates the WireGuard identity at first start.
	// The host must persist the returned base64 key and pass it back via
	// Config.PrivateKey on later runs: re-registering the same device name
	// under a different key is rejected by the control plane, which would
	// permanently brick the TV's network membership. Empty string before the
	// engine has started. Caller frees with TVFree.
	return C.CString(privateKey())
}

// privateKey backs TVPrivateKey; kept Go-side because cgo cannot be used in
// _test.go files, so the not-started behavior is tested through this helper.
func privateKey() string {
	if e := getEngine(); e != nil {
		return e.PrivateKey()
	}
	return ""
}

//export TVOpenURL
func TVOpenURL(url *C.char, handleOut *unsafe.Pointer) *C.char {
	e := getEngine()
	if e == nil {
		return C.CString("engine not started")
	}
	h, err := newMediaHandle(C.GoString(url), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return e.Dial(ctx, network, addr)
	})
	if err != nil {
		return C.CString("open: " + err.Error())
	}
	*handleOut = registerHandle(h)
	return nil
}

//export TVTotalSize
func TVTotalSize(handle unsafe.Pointer) C.int64_t {
	return C.int64_t((*mediaHandle)(handle).size)
}

//export TVReadAt
func TVReadAt(handle unsafe.Pointer, offset C.int64_t, length C.int, buf *C.char) C.int {
	if length < 0 {
		// unsafe.Slice would panic below; never crash across FFI.
		return -1
	}
	h := (*mediaHandle)(handle)
	out := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(length))
	return C.int(h.readAt(int64(offset), out))
}

//export TVClose
func TVClose(handle unsafe.Pointer) {
	unregisterHandle(handle) // drop the GC root before closing
	(*mediaHandle)(handle).close()
}

//export TVFree
func TVFree(p *C.char) {
	C.free(unsafe.Pointer(p))
}

// main is never called: -buildmode=c-archive requires a main package
// (verified 2026-09-30 — the build fails with "-buildmode=c-archive
// requires exactly one main package"; see task-2-report.md §Fix round 1).
func main() {}
