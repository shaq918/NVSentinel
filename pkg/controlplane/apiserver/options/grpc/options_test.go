// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package grpc

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	cliflag "k8s.io/component-base/cli/flag"
)

func TestAddFlags(t *testing.T) {
	o := NewOptions()
	fss := &cliflag.NamedFlagSets{}
	o.AddFlags(fss)

	fs := fss.FlagSet("grpc")
	args := []string{
		"--bind-address=unix:///tmp/test.sock",
		"--max-streams-per-connection=500",
		"--max-recv-msg-size=1048576",
		"--max-send-msg-size=2097152",
		"--grpc-keepalive-time=30s",
		"--grpc-keepalive-timeout=5s",
	}
	fs.Parse(args)

	expected := &Options{
		BindAddress:          "unix:///tmp/test.sock",
		MaxConcurrentStreams: 500,
		MaxRecvMsgSize:       1048576,
		MaxSendMsgSize:       2097152,
		MaxConnectionIdle:    5 * time.Minute,
		KeepAliveTime:        30 * time.Second,
		KeepAliveTimeout:     5 * time.Second,
		MinPingInterval:      5 * time.Second,
		PermitWithoutStream:  true,
	}

	if !reflect.DeepEqual(expected, o) {
		t.Errorf("Difference detected:\n%s", cmp.Diff(expected, o))
	}
}

func TestComplete(t *testing.T) {
	t.Run("Default assignments", func(t *testing.T) {
		o := &Options{}
		completed, err := o.Complete()
		if err != nil {
			t.Fatalf("Complete failed: %v", err)
		}

		if completed.BindAddress != "unix:///var/run/nvidia-device-api/device-api.sock" {
			t.Errorf("expected default bind-address, got %s", completed.BindAddress)
		}
		if completed.MaxConcurrentStreams != 250 {
			t.Errorf("expected default streams 250, got %d", completed.MaxConcurrentStreams)
		}
		if completed.MaxRecvMsgSize != 4194304 {
			t.Errorf("expected default recv size 4MiB, got %d", completed.MaxRecvMsgSize)
		}
		// PermitWithoutStream defaults to true via NewOptions(), not forced by Complete().
		// A zero-value Options{} will have PermitWithoutStream=false since there is no flag for it.
	})

	t.Run("Preserve user overrides", func(t *testing.T) {
		o := NewOptions()
		o.MaxConcurrentStreams = 500

		completed, _ := o.Complete()
		if completed.MaxConcurrentStreams != 500 {
			t.Errorf("User override was lost: got %d", completed.MaxConcurrentStreams)
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
			name: "Invalid scheme",
			modify: func(o *Options) {
				o.BindAddress = "tcp://127.0.0.1:8080"
			},
			wantErr:     true,
			errContains: "must start with \"unix://\"",
		},
		{
			name: "Keepalive Timeout too high",
			modify: func(o *Options) {
				o.KeepAliveTime = 30 * time.Second
				o.KeepAliveTimeout = 40 * time.Second
			},
			wantErr:     true,
			errContains: "must be less than grpc-keepalive-time",
		},
		{
			name: "MaxRecvMsgSize too low",
			modify: func(o *Options) {
				o.MaxRecvMsgSize = 512
			},
			wantErr:     true,
			errContains: "must be between 1024 and 4194304",
		},
		{
			name: "MaxRecvMsgSize too high",
			modify: func(o *Options) {
				o.MaxRecvMsgSize = 10 * 1024 * 1024
			},
			wantErr:     true,
			errContains: "must be between 1024 and 4194304",
		},
		{
			name: "Min Ping Interval violation",
			modify: func(o *Options) {
				o.MinPingInterval = 1 * time.Second
			},
			wantErr:     true,
			errContains: "must be at least 5s",
		},
		{
			name: "Keepalive Time too aggressive",
			modify: func(o *Options) {
				o.KeepAliveTime = 5 * time.Second
			},
			wantErr:     true,
			errContains: "must be 10s or greater",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewOptions()
			tt.modify(o)

			completed, err := o.Complete()
			if err != nil {
				t.Fatalf("Complete failed during setup: %v", err)
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
	o := NewOptions()
	completed, _ := o.Complete()

	var bindAddr string
	var serverOpts []grpc.ServerOption

	err := completed.ApplyTo(&bindAddr, &serverOpts)
	if err != nil {
		t.Fatalf("ApplyTo failed: %v", err)
	}

	if bindAddr != completed.BindAddress {
		t.Errorf("Bind address not applied: got %s, want %s", bindAddr, completed.BindAddress)
	}

	if len(serverOpts) != 5 {
		t.Errorf("Expected 5 server options, got %d", len(serverOpts))
	}
}
