//  Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package storagebackend

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k3s-io/kine/pkg/endpoint"
	"github.com/nvidia/nvsentinel/pkg/util/testutils"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestStorage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketFile := testutils.NewUnixAddr(t)
	socketURL := "unix://" + socketFile

	s := &Storage{
		KineSocketPath: socketURL,
		DatabaseDir:    tmpDir,
		KineConfig: endpoint.Config{
			Listener:         socketURL,
			Endpoint:         "sqlite://" + dbPath,
			CompactBatchSize: 100,
		},
	}

	runCtx, stop := context.WithCancel(context.Background())
	defer stop()

	ps, err := s.PrepareRun(runCtx)
	if err != nil {
		t.Fatalf("PrepareRun failed: %v", err)
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- ps.Run(runCtx)
	}()

	waitErr := wait.PollUntilContextTimeout(runCtx, 100*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		return s.IsReady(), nil
	})
	if waitErr != nil {
		t.Fatal("Timed out waiting for storage to become ready")
	}

	info, err := os.Stat(socketFile)
	if err != nil {
		t.Fatalf("Socket file not found: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0660 {
		t.Errorf("Expected socket mode 0660, got %v", mode)
	}

	stop()

	select {
	case <-runErr:
		// Clean exit
	case <-time.After(2 * time.Second):
		t.Error("Storage did not shut down gracefully")
	}

	if _, err := os.Stat(socketFile); !os.IsNotExist(err) {
		t.Error("Socket file was not cleaned up after shutdown")
	}
}

func TestStorage_SocketInUse(t *testing.T) {
	tmpDir := t.TempDir()
	socketFile := testutils.NewUnixAddr(t)
	socketURL := "unix://" + socketFile

	l, err := net.Listen("unix", socketFile)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	s := &Storage{
		KineSocketPath: socketURL,
		DatabaseDir:    tmpDir,
	}

	err = s.prepareFilesystem(context.Background())
	if err == nil {
		t.Fatal("Expected error because socket is in use, but got nil")
	}

	expectedMsg := "is already in use"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("Expected error containing %q, got: %v", expectedMsg, err)
	}
}

func TestStorage_InMemoryMode(t *testing.T) {
	s := &Storage{InMemory: true}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ps, err := s.PrepareRun(ctx)
	if err != nil {
		t.Fatalf("PrepareRun failed: %v", err)
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- ps.Run(ctx)
	}()

	// In-memory should become ready almost immediately.
	waitErr := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		return s.IsReady(), nil
	})
	if waitErr != nil {
		t.Fatal("In-memory storage did not become ready")
	}

	cancel()

	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Error("In-memory storage did not shut down gracefully")
	}

	if s.IsReady() {
		t.Error("In-memory storage should not be ready after shutdown")
	}
}

func TestStorage_WaitForSocket_Timeout(t *testing.T) {
	socketPath := testutils.NewUnixAddr(t)
	socketURL := "unix://" + socketPath

	s := &Storage{
		KineSocketPath: socketURL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := s.waitForSocket(ctx)

	if err == nil {
		t.Fatal("Expected waitForSocket to timeout, but it succeeded")
	}

	if s.IsReady() {
		t.Error("IsReady should remain false after a failed waitForSocket call")
	}
}
