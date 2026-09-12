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

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPidFile_WriteRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.pid")
	if err := Write(path, 4242); err != nil {
		t.Fatalf("write: %v", err)
	}
	pid, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("pid = %d, want 4242", pid)
	}
}

func TestPidFile_ReadMissingIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.pid")
	if _, err := Read(path); err == nil {
		t.Fatal("expected error for missing pidfile")
	}
}

func TestPidFile_IsAlive(t *testing.T) {
	self := os.Getpid()
	if !IsAlive(self) {
		t.Fatal("own process must be alive")
	}
	if IsAlive(999999) {
		t.Fatal("bogus pid must not be alive")
	}
}

func TestPidFile_Remove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.pid")
	if err := Write(path, 1); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile should be gone")
	}
}
