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

	"github.com/k3s-io/kine/pkg/endpoint"
	"github.com/nvidia/nvsentinel/pkg/storage/storagebackend/options"
	apistorage "k8s.io/apiserver/pkg/storage/storagebackend"
)

type Config struct {
	KineConfig     endpoint.Config
	KineSocketPath string
	DatabaseDir    string

	// InMemory skips Kine/SQLite entirely. Services supply their own
	// in-memory storage.Interface, so the backend only needs to report ready.
	InMemory bool

	StorageConfig apistorage.Config
}

type CompletedConfig struct {
	*Config
}

func NewConfig(ctx context.Context, opts options.CompletedOptions) (*Config, error) {
	config := &Config{
		KineConfig:     opts.KineConfig,
		KineSocketPath: opts.KineSocketPath,
		DatabaseDir:    opts.DatabaseDir,
		InMemory:       opts.InMemory,
	}

	if err := opts.ApplyTo(&config.StorageConfig); err != nil {
		return nil, fmt.Errorf("failed to apply storage options: %w", err)
	}

	return config, nil
}

func (c *Config) Complete() (CompletedConfig, error) {
	if c == nil {
		return CompletedConfig{}, nil
	}

	return CompletedConfig{c}, nil
}
