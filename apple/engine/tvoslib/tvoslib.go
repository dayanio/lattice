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

package tvoslib

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
	*handleOut = unsafe.Pointer(h)
	return nil
}

//export TVTotalSize
func TVTotalSize(handle unsafe.Pointer) C.int64_t {
	return C.int64_t((*mediaHandle)(handle).size)
}

//export TVReadAt
func TVReadAt(handle unsafe.Pointer, offset C.int64_t, length C.int, buf *C.char) C.int {
	h := (*mediaHandle)(handle)
	out := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(length))
	return C.int(h.readAt(int64(offset), out))
}

//export TVClose
func TVClose(handle unsafe.Pointer) {
	(*mediaHandle)(handle).close()
}

//export TVFree
func TVFree(p *C.char) {
	C.free(unsafe.Pointer(p))
}

func main() {}
