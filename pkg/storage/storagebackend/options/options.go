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
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/k3s-io/kine/pkg/endpoint"
	nvvalidation "github.com/nvidia/nvsentinel/pkg/util/validation"
	"k8s.io/apiserver/pkg/server/options"
	apistorage "k8s.io/apiserver/pkg/storage/storagebackend"
	cliflag "k8s.io/component-base/cli/flag"
)

type Options struct {
	// InMemory skips the Kine/SQLite storage backend entirely.
	// When true, services provide their own in-memory storage.Interface.
	InMemory bool

	DatabasePath                string
	CompactionInterval          time.Duration
	CompactionBatchSize         int64
	WatchProgressNotifyInterval time.Duration

	KineConfig     endpoint.Config
	KineSocketPath string
	DatabaseDir    string
	Etcd           *options.EtcdOptions
}

type completedOptions struct {
	Options
}

type CompletedOptions struct {
	*completedOptions
}

func NewOptions() *Options {
	return &Options{
		InMemory:                    true,
		DatabasePath:                "/var/lib/nvidia-device-api/state.db",
		CompactionInterval:          5 * time.Minute,
		CompactionBatchSize:         1000,
		WatchProgressNotifyInterval: 5 * time.Second,
		Etcd:                        options.NewEtcdOptions(apistorage.NewDefaultConfig("/registry", nil)),
	}
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	if o == nil {
		return
	}

	storageFs := fss.FlagSet("storage")

	storageFs.BoolVar(&o.InMemory, "in-memory", o.InMemory,
		"Use in-memory storage instead of SQLite/Kine. Services provide their own storage.Interface.")

	storageFs.StringVar(&o.DatabasePath, "database-path", o.DatabasePath,
		"The path to the SQLite database file. Must be an absolute path.")

	storageFs.DurationVar(&o.CompactionInterval, "compaction-interval", o.CompactionInterval,
		"The interval of compaction requests. If 0, compaction is disabled. If enabled, must be at least 1m.")
	storageFs.Int64Var(&o.CompactionBatchSize, "compaction-batch-size", o.CompactionBatchSize,
		"Number of revisions to compact in a single batch. Must be between 1 and 10000.")
	storageFs.DurationVar(&o.WatchProgressNotifyInterval, "watch-progress-notify-interval", o.WatchProgressNotifyInterval,
		"Interval between periodic watch progress notifications. Must be between 5s and 10m.")
}

func (o *Options) Complete() (CompletedOptions, error) {
	if o == nil {
		return CompletedOptions{}, nil
	}

	// In-memory mode skips all Kine/SQLite configuration.
	if o.InMemory {
		completed := completedOptions{Options: *o}
		return CompletedOptions{completedOptions: &completed}, nil
	}

	if o.KineSocketPath == "" {
		o.KineSocketPath = "/var/run/nvidia-device-api/kine.sock"
	}
	o.KineSocketPath = strings.TrimPrefix(o.KineSocketPath, "unix://")

	if o.KineConfig.Listener == "" {
		o.KineConfig.Listener = "unix://" + o.KineSocketPath
	}

	if o.DatabasePath == "" {
		o.DatabasePath = "/var/lib/nvidia-device-api/state.db"
	}
	o.DatabaseDir = filepath.Dir(o.DatabasePath)

	if o.KineConfig.Endpoint == "" {
		o.KineConfig.Endpoint = fmt.Sprintf(
			"sqlite://%s?_journal=WAL&_timeout=5000&_synchronous=NORMAL&_fk=1",
			o.DatabasePath,
		)
	}

	o.KineConfig.CompactInterval = o.CompactionInterval
	o.KineConfig.CompactBatchSize = o.CompactionBatchSize
	o.KineConfig.NotifyInterval = o.WatchProgressNotifyInterval

	o.Etcd.StorageConfig.HealthcheckTimeout = 10 * time.Second
	o.Etcd.StorageConfig.ReadycheckTimeout = 10 * time.Second

	if len(o.Etcd.StorageConfig.Transport.ServerList) == 0 {
		o.Etcd.StorageConfig.Transport.ServerList = []string{o.KineConfig.Listener}
	}

	completed := completedOptions{
		Options: *o,
	}

	return CompletedOptions{
		completedOptions: &completed,
	}, nil
}

//nolint:gocyclo,cyclop
func (o *Options) Validate() []error {
	if o == nil {
		return nil
	}

	// In-memory mode requires no Kine/SQLite configuration.
	if o.InMemory {
		return nil
	}

	allErrors := []error{}

	if o.DatabasePath == "" {
		allErrors = append(allErrors, fmt.Errorf("database-path: required"))
	} else if !filepath.IsAbs(o.DatabasePath) {
		allErrors = append(allErrors, fmt.Errorf("database-path %q: must be an absolute path", o.DatabasePath))
	}

	if o.DatabaseDir == "" {
		allErrors = append(allErrors, fmt.Errorf("database directory: not initialized"))
	}

	if o.KineSocketPath == "" {
		allErrors = append(allErrors, fmt.Errorf("kine-socket-path: not initialized"))
	} else if !filepath.IsAbs(o.KineSocketPath) {
		allErrors = append(allErrors, fmt.Errorf("kine-socket-path %q: must be an absolute path", o.KineSocketPath))
	}

	if o.KineConfig.Listener == "" {
		allErrors = append(allErrors, fmt.Errorf("kine-listener: required"))
	} else {
		if validationErrors := nvvalidation.IsUnixSocketURI(o.KineConfig.Listener); len(validationErrors) > 0 {
			for _, errDesc := range validationErrors {
				allErrors = append(allErrors, fmt.Errorf("kine-listener %q: %s", o.KineConfig.Listener, errDesc))
			}
		}

		actualPath := strings.TrimPrefix(o.KineConfig.Listener, "unix://")
		if actualPath != o.KineSocketPath {
			allErrors = append(allErrors,
				fmt.Errorf("kine-listener path %q: does not match kine-socket-path %q",
					actualPath,
					o.KineSocketPath))
		}
	}

	if o.CompactionInterval > 0 && o.CompactionInterval < 1*time.Minute {
		allErrors = append(allErrors,
			fmt.Errorf("compaction-interval: %v must be 1m or greater (or 0 to disable)",
				o.CompactionInterval))
	} else if o.CompactionInterval < 0 {
		allErrors = append(allErrors,
			fmt.Errorf("compaction-interval: %v must be 0s or greater",
				o.CompactionInterval))
	}

	if o.CompactionBatchSize <= 0 {
		allErrors = append(allErrors,
			fmt.Errorf("compaction-batch-size: %v must be greater than 0",
				o.CompactionBatchSize))
	} else if o.CompactionBatchSize > 10000 {
		allErrors = append(allErrors,
			fmt.Errorf("compaction-batch-size: %v must be 10000 or less",
				o.CompactionBatchSize))
	}

	if o.WatchProgressNotifyInterval < 5*time.Second {
		allErrors = append(allErrors,
			fmt.Errorf("watch-progress-notify-interval: %v must be 5s or greater",
				o.WatchProgressNotifyInterval))
	} else if o.WatchProgressNotifyInterval > 10*time.Minute {
		allErrors = append(allErrors,
			fmt.Errorf("watch-progress-notify-interval: %v must be 10m or less",
				o.WatchProgressNotifyInterval))
	}

	if o.Etcd != nil {
		allErrors = append(allErrors, o.Etcd.Validate()...)
	}

	return allErrors
}

func (o *Options) ApplyTo(storageConfig *apistorage.Config) error {
	if o == nil {
		return nil
	}

	*storageConfig = o.Etcd.StorageConfig

	return nil
}
