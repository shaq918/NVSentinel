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

package net

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
)

// CreateUDSListener creates a net.Listener for a Unix Domain Socket at the specified path.
// It handles directory creation, stale socket removal by checking for active listeners,
// and sets the requested file permissions. It returns the listener, a cleanup function
// to close the listener and remove the socket file, or an error.
func CreateUDSListener(ctx context.Context, socketPath string, perm os.FileMode) (net.Listener, func(), error) {
	socketDir := filepath.Dir(socketPath)
	if err := os.MkdirAll(socketDir, 0750); err != nil {
		return nil, nil, fmt.Errorf("failed to create socket directory %q: %w", socketDir, err)
	}

	if _, err := os.Stat(socketPath); err == nil {
		d := net.Dialer{Timeout: 100 * time.Millisecond}

		conn, dialErr := d.DialContext(ctx, "unix", socketPath)
		if dialErr == nil {
			conn.Close()
			return nil, nil, fmt.Errorf("socket %q is already in use", socketPath)
		}

		klog.V(2).Info("Removing stale socket file", "path", socketPath)

		if err := CleanupUDS(socketPath); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("failed to stat socket %q: %w", socketPath, err)
	}

	lc := net.ListenConfig{}

	// Note: There is a residual TOCTOU window between CleanupUDS and Listen.
	// This is acceptable because Listen will fail with EADDRINUSE if another
	// process binds the socket in that window.
	lis, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen on unix socket %q: %w", socketPath, err)
	}

	if err := os.Chmod(socketPath, perm); err != nil {
		lis.Close()
		return nil, nil, fmt.Errorf("failed to secure socket %q: %w", socketPath, err)
	}

	cleanup := func() {
		lis.Close()

		if err := CleanupUDS(socketPath); err != nil {
			klog.V(2).ErrorS(err, "Failed to cleanup socket", "path", socketPath)
		}
	}

	return lis, cleanup, nil
}

// CleanupUDS removes the Unix Domain Socket file at the specified path.
// It returns an error if the file exists but cannot be removed. If the
// file does not exist, it is considered a success and returns nil.
func CleanupUDS(socketPath string) error {
	if socketPath == "" {
		return nil
	}

	klog.V(2).InfoS("Removing socket file", "path", socketPath)

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove socket file %q: %w", socketPath, err)
	}

	return nil
}
