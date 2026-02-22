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
	"testing"

	"github.com/nvidia/nvsentinel/pkg/storage/storagebackend/options"
)

func TestNewConfig(t *testing.T) {
	ctx := context.Background()

	opts := options.NewOptions()
	opts.InMemory = false
	opts.DatabasePath = "/tmp/nvsentinel/test.db"

	completedOpts, err := opts.Complete()
	if err != nil {
		t.Fatalf("failed to complete options: %v", err)
	}

	cfg, err := NewConfig(ctx, completedOpts)
	if err != nil {
		t.Fatalf("NewConfig failed: %v", err)
	}

	if cfg.DatabaseDir != completedOpts.DatabaseDir {
		t.Errorf("DatabaseDir mismatch: got %v, want %v", cfg.DatabaseDir, completedOpts.DatabaseDir)
	}

	if cfg.KineSocketPath != completedOpts.KineSocketPath {
		t.Errorf("KineSocketPath mismatch: got %v, want %v", cfg.KineSocketPath, completedOpts.KineSocketPath)
	}

	if len(cfg.StorageConfig.Transport.ServerList) == 0 {
		t.Error("StorageConfig.Transport.ServerList is empty; ApplyTo logic failed")
	}

	expectedListener := completedOpts.KineConfig.Listener
	if cfg.StorageConfig.Transport.ServerList[0] != expectedListener {
		t.Errorf("StorageConfig server mismatch: got %v, want %v",
			cfg.StorageConfig.Transport.ServerList[0], expectedListener)
	}
}

func TestComplete(t *testing.T) {
	var nilCfg *Config
	completed, err := nilCfg.Complete()
	if err != nil {
		t.Errorf("Complete on nil config should not error, got %v", err)
	}
	if completed.Config != nil {
		t.Error("CompletedConfig should wrap a nil pointer if source is nil")
	}

	cfg := &Config{KineSocketPath: "/test/path"}
	completed, err = cfg.Complete()
	if err != nil {
		t.Errorf("Complete failed: %v", err)
	}
	if completed.KineSocketPath != "/test/path" {
		t.Errorf("CompletedConfig did not preserve data")
	}
}
