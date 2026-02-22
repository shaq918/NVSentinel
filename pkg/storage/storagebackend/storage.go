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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/k3s-io/kine/pkg/endpoint"
	netutils "github.com/nvidia/nvsentinel/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	apistorage "k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/klog/v2"
)

type Storage struct {
	KineConfig     endpoint.Config
	KineSocketPath string
	DatabaseDir    string

	StorageConfig apistorage.Config
	ETCDConfig    *endpoint.ETCDConfig

	// InMemory skips Kine/SQLite entirely. When true, the storage backend
	// reports ready immediately and services use their own in-memory storage.
	InMemory bool

	isReady atomic.Bool
}

type preparedStorage struct {
	*Storage
}

func (c *CompletedConfig) New() (*Storage, error) {
	return &Storage{
		KineConfig:     c.KineConfig,
		KineSocketPath: c.KineSocketPath,
		DatabaseDir:    c.DatabaseDir,
		StorageConfig:  c.StorageConfig,
		InMemory:       c.InMemory,
	}, nil
}

func (s *Storage) PrepareRun(ctx context.Context) (preparedStorage, error) {
	if s.InMemory {
		return preparedStorage{s}, nil
	}

	if err := s.prepareFilesystem(ctx); err != nil {
		return preparedStorage{}, err
	}

	return preparedStorage{s}, nil
}

func (s *Storage) prepareFilesystem(ctx context.Context) error {
	if err := os.MkdirAll(s.DatabaseDir, 0750); err != nil {
		return fmt.Errorf("failed to create storage data directory: %w", err)
	}

	socketPath := strings.TrimPrefix(s.KineSocketPath, "unix://")

	socketDir := filepath.Dir(socketPath)
	if err := os.MkdirAll(socketDir, 0750); err != nil {
		return fmt.Errorf("failed to create kine socket directory: %w", err)
	}

	_, err := os.Stat(socketPath)
	if err == nil {
		d := net.Dialer{Timeout: 100 * time.Millisecond}
		conn, dialErr := d.DialContext(ctx, "unix", socketPath) //nolint:wsl_v5
		if dialErr == nil {
			conn.Close()
			return fmt.Errorf("kine socket %q is already in use", socketPath)
		}

		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale kine socket %q: %w", socketPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat kine socket %q: %w", socketPath, err)
	}

	return nil
}

func (s *preparedStorage) Run(ctx context.Context) error {
	return s.run(ctx)
}

func (s *Storage) run(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	if s.InMemory {
		logger.V(2).Info("Starting in-memory storage backend (no persistence)")
		s.isReady.Store(true)
		<-ctx.Done()
		logger.Info("Shutting down in-memory storage backend")
		s.isReady.Store(false)
		return nil
	}

	logger.V(2).Info("Starting storage backend", "database", s.KineConfig.Endpoint)
	s.isReady.Store(false)

	// Restrict permissions on new files (socket) before Kine creates it.
	oldUmask := syscall.Umask(0117) // Creates socket as 0660 from the start
	defer syscall.Umask(oldUmask)

	etcdConfig, err := endpoint.Listen(ctx, s.KineConfig)
	if err != nil {
		return fmt.Errorf("failed to start storage backend: %w", err)
	}

	s.ETCDConfig = &etcdConfig

	socketPath := strings.TrimPrefix(s.KineSocketPath, "unix://")
	defer func() {
		if err := netutils.CleanupUDS(socketPath); err != nil {
			klog.ErrorS(err, "Failed to cleanup kine socket", "path", socketPath)
		}
	}()

	if err := s.waitForSocket(ctx); err != nil {
		return err
	}

	logger.V(3).Info("Storage backend socket is ready", "path", socketPath)
	s.isReady.Store(true)

	<-ctx.Done()
	logger.Info("Shutting down storage backend")
	s.isReady.Store(false)

	return nil
}

func (s *Storage) waitForSocket(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	socketPath := strings.TrimPrefix(s.KineSocketPath, "unix://")

	logger.V(4).Info("Waiting for socket to accept connections", "path", socketPath)

	err := wait.PollUntilContextTimeout(
		ctx,
		200*time.Millisecond,
		30*time.Second,
		true,
		func(ctx context.Context) (bool, error) {
			if _, err := os.Stat(socketPath); err != nil {
				//nolint:nilerr // socket isn't there yet, keep polling
				return false, nil
			}

			d := net.Dialer{Timeout: 100 * time.Millisecond}
			conn, err := d.DialContext(ctx, "unix", socketPath) //nolint:wsl_v5
			if err != nil {
				//nolint:nilerr // socket isn't accepting yet, keep polling
				return false, nil
			}
			conn.Close() //nolint:wsl_v5

			//nolint:gosec // G302: 0660 intentional — server and provider share a group
			if err := os.Chmod(socketPath, 0660); err != nil {
				if os.IsPermission(err) {
					return false, fmt.Errorf("failed to secure kine socket %q: %w", socketPath, err)
				}

				logger.V(4).Error(err, "Failed to secure socket, retrying", "path", socketPath)

				return false, nil
			}

			return true, nil
		},
	)
	if err != nil {
		return fmt.Errorf("timed out waiting to connect to socket: %w", err)
	}

	return nil
}

func (s *Storage) IsReady() bool {
	return s.isReady.Load()
}
