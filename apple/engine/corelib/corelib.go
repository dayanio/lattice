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

// Command corelib is not a real command: the package name must be main
// because -buildmode=c-archive requires exactly one main package (same as
// tvoslib, verified 2026-09-30), while the directory — and the engine
// package it binds — stays corelib. It exports the Core engine
// (apple/engine, the packetTUN/NE variant) over a plain C ABI for the
// macOS/iOS Network Extension, replacing the gomobile bind route. See
// docs/superpowers/specs/2026-10-05-apple-engine-carchive-migration-design.md
// §D3.
package main

/*
#include <stdlib.h>

// Every engine callback carries exactly one string (event name, overlay IP,
// peer-states JSON, routes JSON), so one function-pointer type covers all
// four. The value is consumed synchronously inside the call and freed by the
// Go side on return — Swift must not retain the pointer.
typedef void (*CoreCallbackFn)(const char* value, void* ctx);

static void corelibDispatch(CoreCallbackFn fn, void* ctx, const char* value) {
	if (fn != 0) { fn(value, ctx); }
}
*/
import "C"

import (
	"sync"
	"unsafe"

	coreengine "github.com/alatticeio/lattice/apple/engine"
)

var (
	mu     sync.Mutex
	engine *coreengine.Engine
)

func setEngine(e *coreengine.Engine) { mu.Lock(); engine = e; mu.Unlock() }
func getEngine() *coreengine.Engine  { mu.Lock(); defer mu.Unlock(); return engine }

// cb adapts engine.EngineDelegate to the C ABI. It is package-level and
// outlives any single engine instance, so callbacks that fire during engine
// teardown (the run loop emits "disconnected" on exit) still reach Swift.
type cb struct {
	mu              sync.RWMutex
	onEvent         C.CoreCallbackFn
	onTunnelUp      C.CoreCallbackFn
	onPeerStates    C.CoreCallbackFn
	onRoutesChanged C.CoreCallbackFn
	ctx             unsafe.Pointer
}

func (c *cb) set(onEvent, onTunnelUp, onPeerStates, onRoutesChanged C.CoreCallbackFn, ctx unsafe.Pointer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEvent = onEvent
	c.onTunnelUp = onTunnelUp
	c.onPeerStates = onPeerStates
	c.onRoutesChanged = onRoutesChanged
	c.ctx = ctx
}

// call reads the fn pointer and ctx under one RLock (CoreSetCallbacks can
// race with engine goroutines) and hands the string across synchronously:
// C.CString → invoke → free on return. Same lifetime discipline as tvoslib's
// TVEventFn dispatch.
func (c *cb) call(fn C.CoreCallbackFn, value string) {
	c.mu.RLock()
	ctx := c.ctx
	c.mu.RUnlock()
	if fn == nil {
		return
	}
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))
	C.corelibDispatch(fn, ctx, cValue)
}

func (c *cb) OnEvent(event string) {
	c.mu.RLock()
	fn := c.onEvent
	c.mu.RUnlock()
	c.call(fn, event)
}

func (c *cb) OnTunnelUp(overlayIP string) {
	c.mu.RLock()
	fn := c.onTunnelUp
	c.mu.RUnlock()
	c.call(fn, overlayIP)
}

func (c *cb) OnPeerStates(statesJSON string) {
	c.mu.RLock()
	fn := c.onPeerStates
	c.mu.RUnlock()
	c.call(fn, statesJSON)
}

func (c *cb) OnRoutesChanged(routesJSON string) {
	c.mu.RLock()
	fn := c.onRoutesChanged
	c.mu.RUnlock()
	c.call(fn, routesJSON)
}

//export CoreResetIdentity
func CoreResetIdentity() C.int {
	// Package-level: wipes the persisted WireGuard identity (engine.go's
	// wg-identity.key). Call before CoreNewEngine so the next registration
	// gets a fresh key. 0 on success, -1 on failure.
	if err := coreengine.ResetIdentity(); err != nil {
		return -1
	}
	return 0
}

//export CoreNewEngine
func CoreNewEngine(cfg *C.char) *C.char {
	// Validates and stores the config; nothing runs until CoreStart. Returns
	// nil on success or a caller-freed error string. One engine per process:
	// a second call without CoreStop in between is an error. The delegate is
	// the package-level cb singleton — CoreSetCallbacks configures it
	// independently of the engine lifecycle, and it outlives CoreStop so
	// teardown-time callbacks still reach Swift.
	mu.Lock()
	defer mu.Unlock()
	if engine != nil {
		return C.CString("engine already created; call CoreStop first")
	}
	e, err := coreengine.NewEngine(C.GoString(cfg), currentCB)
	if err != nil {
		return C.CString("config: " + err.Error())
	}
	engine = e
	return nil
}

//export CoreSetCallbacks
func CoreSetCallbacks(onEvent, onTunnelUp, onPeerStates, onRoutesChanged C.CoreCallbackFn, ctx unsafe.Pointer) {
	// Installable before CoreNewEngine (typical Swift order: set callbacks,
	// create, start) and re-installable while running; nil fn = drop that
	// callback.
	currentCB.set(onEvent, onTunnelUp, onPeerStates, onRoutesChanged, ctx)
}

// currentCB is the delegate bound to every engine instance. Singleton like
// the engine itself: one tunnel session per process (NE appex).
var currentCB = &cb{}

//export CoreStart
func CoreStart() *C.char {
	// Launches the engine in the background; registration and data-plane
	// bring-up are reported through the callbacks. nil on success.
	e := getEngine()
	if e == nil {
		return C.CString("engine not created")
	}
	if err := e.Start(); err != nil {
		return C.CString("start: " + err.Error())
	}
	return nil
}

//export CoreStop
func CoreStop() {
	// Tears the engine down (blocks until the run loop exits, bounded 10s)
	// and clears the singleton; CoreNewEngine may be called again after.
	// The singleton is cleared first so re-entrant lookups during teardown
	// see a stopped engine.
	e := getEngine()
	setEngine(nil)
	if e != nil {
		_ = e.Stop()
	}
}

//export CorePublicKey
func CorePublicKey() *C.char {
	// Base64 WireGuard public key, generated and persisted on first use.
	// Empty string before the engine exists.
	e := getEngine()
	if e == nil {
		return C.CString("")
	}
	return C.CString(e.PublicKey())
}

//export CorePeers
func CorePeers() *C.char {
	// Netmap snapshot as JSON; empty string before the engine exists.
	// Caller frees with CoreFree.
	e := getEngine()
	if e == nil {
		return C.CString("")
	}
	return C.CString(e.Peers())
}

//export CoreSetUpstreamDNS
func CoreSetUpstreamDNS(dns *C.char) *C.char {
	e := getEngine()
	if e == nil {
		return C.CString("engine not created")
	}
	e.SetUpstreamDNS(C.GoString(dns))
	return nil
}

//export CoreSetSplitRouting
func CoreSetSplitRouting(on C.int) *C.char {
	e := getEngine()
	if e == nil {
		return C.CString("engine not created")
	}
	e.SetSplitRouting(on != 0)
	return nil
}

//export CorePublishCastCommand
func CorePublishCastCommand(appID, payload *C.char) *C.char {
	e := getEngine()
	if e == nil {
		return C.CString("engine not created")
	}
	if err := e.PublishCastCommand(C.GoString(appID), C.GoString(payload)); err != nil {
		return C.CString("publish: " + err.Error())
	}
	return nil
}

//export CoreSendPacketBatch
func CoreSendPacketBatch(data *C.char, length C.int) C.int {
	// Zero-copy: wraps the caller's buffer with unsafe.Slice and hands it
	// straight to SendPacketBatch, which consumes the frames synchronously
	// (engine.go parses and writes into wireguard-go before returning) — the
	// pointer is never retained. 0 on success, -1 on error.
	if length < 0 {
		// unsafe.Slice would panic below; never crash across FFI.
		return -1
	}
	e := getEngine()
	if e == nil {
		// No engine = nothing to inject into. Drop silently: the NE
		// teardown race (packets still flowing after CoreStop) is normal,
		// not an error worth surfacing.
		return 0
	}
	blob := unsafe.Slice((*byte)(unsafe.Pointer(data)), int(length))
	if err := e.SendPacketBatch(blob); err != nil {
		return -1
	}
	return 0
}

//export CoreFree
func CoreFree(p *C.char) {
	// Frees strings returned by CoreNewEngine/CoreStart/CorePeers/
	// CorePublicKey/... error and value returns.
	C.free(unsafe.Pointer(p))
}

// main is never called: -buildmode=c-archive requires a main package
// (same as tvoslib).
func main() {}
