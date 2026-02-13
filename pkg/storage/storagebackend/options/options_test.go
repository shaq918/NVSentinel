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

package options

import (
	"strings"
	"testing"
	"time"

	apistorage "k8s.io/apiserver/pkg/storage/storagebackend"
	cliflag "k8s.io/component-base/cli/flag"
)

func TestAddFlags(t *testing.T) {
	o := NewOptions()
	fss := &cliflag.NamedFlagSets{}
	o.AddFlags(fss)

	fs := fss.FlagSet("storage")
	args := []string{
		"--database-path=/tmp/custom.db",
		"--compaction-interval=2m",
		"--compaction-batch-size=5000",
		"--watch-progress-notify-interval=30s",
	}

	err := fs.Parse(args)
	if err != nil {
		t.Fatalf("Failed to parse flags: %v", err)
	}

	if o.DatabasePath != "/tmp/custom.db" {
		t.Errorf("expected DatabasePath %s, got %s", "/tmp/custom.db", o.DatabasePath)
	}

	if o.CompactionInterval != 2*time.Minute {
		t.Errorf("expected CompactionInterval %v, got %v", 2*time.Minute, o.CompactionInterval)
	}

	if o.CompactionBatchSize != 5000 {
		t.Errorf("expected CompactionBatchSize %d, got %d", 5000, o.CompactionBatchSize)
	}

	if o.WatchProgressNotifyInterval != 30*time.Second {
		t.Errorf("expected WatchProgressNotifyInterval %v, got %v", 30*time.Second, o.WatchProgressNotifyInterval)
	}
}

func TestComplete(t *testing.T) {
	t.Run("Default assignments", func(t *testing.T) {
		opts := NewOptions()
		opts.InMemory = false
		opts.DatabasePath = ""
		opts.KineSocketPath = ""

		completed, err := opts.Complete()
		if err != nil {
			t.Fatalf("Complete failed: %v", err)
		}

		if completed.KineSocketPath != "/var/run/nvidia-device-api/kine.sock" {
			t.Errorf("expected default socket path, got %s", completed.KineSocketPath)
		}
		if completed.KineConfig.Listener != "unix:///var/run/nvidia-device-api/kine.sock" {
			t.Errorf("expected default listener URI, got %s", completed.KineConfig.Listener)
		}
		if !strings.Contains(completed.KineConfig.Endpoint, "sqlite:///var/lib/nvidia-device-api/state.db") {
			t.Errorf("DSN not properly constructed: %s", completed.KineConfig.Endpoint)
		}
		if completed.DatabaseDir != "/var/lib/nvidia-device-api" {
			t.Errorf("DatabaseDir not derived correctly: %s", completed.DatabaseDir)
		}
	})

	t.Run("Trims unix prefix from SocketPath", func(t *testing.T) {
		opts := NewOptions()
		opts.InMemory = false
		opts.KineSocketPath = "unix:///tmp/test.sock"

		completed, _ := opts.Complete()
		if completed.KineSocketPath != "/tmp/test.sock" {
			t.Errorf("Complete should trim prefix from SocketPath: got %s", completed.KineSocketPath)
		}
	})

	t.Run("Maps intervals to KineConfig", func(t *testing.T) {
		opts := NewOptions()
		opts.InMemory = false
		opts.CompactionInterval = 10 * time.Minute
		opts.WatchProgressNotifyInterval = 15 * time.Second

		completed, _ := opts.Complete()
		if completed.KineConfig.CompactInterval != 10*time.Minute {
			t.Error("CompactInterval not mapped to KineConfig")
		}
		if completed.KineConfig.NotifyInterval != 15*time.Second {
			t.Error("NotifyInterval not mapped to KineConfig")
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(*Options)
		wantErr     bool
		errContains string
	}{
		{
			name:    "Valid defaults",
			modify:  func(o *Options) {},
			wantErr: false,
		},
		{
			name: "Relative database path",
			modify: func(o *Options) {
				o.DatabasePath = "relative/path.db"
			},
			wantErr:     true,
			errContains: "must be an absolute path",
		},
		{
			name: "Compaction interval too low (but > 0)",
			modify: func(o *Options) {
				o.CompactionInterval = 30 * time.Second
			},
			wantErr:     true,
			errContains: "must be 1m or greater",
		},
		{
			name: "Compaction interval 0 is allowed",
			modify: func(o *Options) {
				o.CompactionInterval = 0
			},
			wantErr: false,
		},
		{
			name: "Compaction batch size too large",
			modify: func(o *Options) {
				o.CompactionBatchSize = 50000
			},
			wantErr:     true,
			errContains: "must be 10000 or less",
		},
		{
			name: "Watch notify interval below floor",
			modify: func(o *Options) {
				o.WatchProgressNotifyInterval = 1 * time.Second
			},
			wantErr:     true,
			errContains: "must be 5s or greater",
		},
		{
			name: "Watch notify interval above ceiling",
			modify: func(o *Options) {
				o.WatchProgressNotifyInterval = 30 * time.Minute
			},
			wantErr:     true,
			errContains: "must be 10m or less",
		},
		{
			name: "Socket path/URI mismatch",
			modify: func(o *Options) {
				o.KineSocketPath = "/path/a.sock"
				o.KineConfig.Listener = "unix:///path/b.sock"
			},
			wantErr:     true,
			errContains: "does not match kine-socket-path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.InMemory = false
			tt.modify(opts)

			completed, err := opts.Complete()
			if err != nil {
				t.Fatalf("Complete() failed: %v", err)
			}
			errs := completed.Validate()

			if (len(errs) > 0) != tt.wantErr {
				t.Errorf("Validate() errors = %v, wantErr %v", errs, tt.wantErr)
			}

			if tt.wantErr && len(errs) > 0 {
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), tt.errContains) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("None of the errors %v contain %q", errs, tt.errContains)
				}
			}
		})
	}
}

func TestApplyTo(t *testing.T) {
	opts := NewOptions()
	opts.InMemory = false
	completed, _ := opts.Complete()

	storageCfg := &apistorage.Config{}
	err := completed.ApplyTo(storageCfg)
	if err != nil {
		t.Fatalf("ApplyTo failed: %v", err)
	}

	if len(storageCfg.Transport.ServerList) == 0 {
		t.Error("ApplyTo failed to populate ServerList")
	}

	if storageCfg.Transport.ServerList[0] != completed.KineConfig.Listener {
		t.Errorf("StorageConfig server mismatch. Got %v, want %v",
			storageCfg.Transport.ServerList[0], completed.KineConfig.Listener)
	}
}
