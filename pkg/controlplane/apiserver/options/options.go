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

package options

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	grpc "github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/options/grpc"
	storagebackend "github.com/nvidia/nvsentinel/pkg/storage/storagebackend/options"
	nvvalidation "github.com/nvidia/nvsentinel/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
)

type Options struct {
	NodeName             string
	HealthAddress        string
	ServiceMonitorPeriod time.Duration
	MetricsAddress       string
	ShutdownGracePeriod  time.Duration

	GRPC    *grpc.Options
	Storage *storagebackend.Options
	Logs    *logs.Options
}

type completedOptions struct {
	NodeName             string
	HealthAddress        string
	ServiceMonitorPeriod time.Duration
	MetricsAddress       string
	ShutdownGracePeriod  time.Duration

	GRPC    grpc.CompletedOptions
	Storage storagebackend.CompletedOptions
	Logs    *logs.Options
}

type CompletedOptions struct {
	*completedOptions
}

func NewOptions() *Options {
	return &Options{
		ServiceMonitorPeriod: 10 * time.Second,
		ShutdownGracePeriod:  25 * time.Second,
		GRPC:                 grpc.NewOptions(),
		Storage:              storagebackend.NewOptions(),
		Logs:                 logs.NewOptions(),
	}
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	if o == nil {
		return
	}

	genericFs := fss.FlagSet("generic")

	genericFs.StringVar(&o.NodeName, "hostname-override", o.NodeName,
		"If non-empty, will use this string as identification instead of the actual hostname. "+
			"Must be a valid DNS subdomain.")

	genericFs.StringVar(&o.HealthAddress, "health-probe-bind-address", o.HealthAddress,
		"The TCP address (IP:port) to serve gRPC health and reflection. "+
			"If empty, defaults to :50051.")
	genericFs.DurationVar(&o.ServiceMonitorPeriod, "service-monitor-period", o.ServiceMonitorPeriod,
		"The period for syncing internal service status."+
			"Must be between 0s and 1m.")

	genericFs.StringVar(&o.MetricsAddress, "metrics-bind-address", o.MetricsAddress,
		"The TCP address (IP:port) to serve HTTP metrics. "+
			"If empty, defaults to :9090.")

	genericFs.DurationVar(&o.ShutdownGracePeriod, "shutdown-grace-period", o.ShutdownGracePeriod,
		"The maximum duration to wait for the server to shut down gracefully before forcing a stop. "+
			"Must be between 0s and 10m.")

	o.GRPC.AddFlags(fss)
	o.Storage.AddFlags(fss)
	logsapi.AddFlags(o.Logs, fss.FlagSet("logs"))
}

//nolint:cyclop
func (o *Options) Complete(ctx context.Context) (CompletedOptions, error) {
	if o == nil {
		return CompletedOptions{completedOptions: &completedOptions{}}, nil
	}

	if o.NodeName == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			hostname = os.Getenv("NODE_NAME")
		}

		o.NodeName = hostname
	}
	o.NodeName = strings.ToLower(strings.TrimSpace(o.NodeName)) //nolint:wsl

	if o.HealthAddress == "" {
		// Default binds to all interfaces for Kubernetes kubelet health probes.
		// Use NetworkPolicy to restrict access in production.
		o.HealthAddress = ":50051"
	}

	if o.ServiceMonitorPeriod == 0 {
		o.ServiceMonitorPeriod = 10 * time.Second
	}

	if o.MetricsAddress == "" {
		// Default binds to all interfaces for Prometheus scraping.
		// Use NetworkPolicy to restrict access in production.
		o.MetricsAddress = ":9090"
	}

	if o.ShutdownGracePeriod == 0 {
		o.ShutdownGracePeriod = 25 * time.Second
	}

	completedGRPC, err := o.GRPC.Complete()
	if err != nil {
		return CompletedOptions{}, err
	}

	completedStorage, err := o.Storage.Complete()
	if err != nil {
		return CompletedOptions{}, err
	}

	completed := completedOptions{
		NodeName:             o.NodeName,
		HealthAddress:        o.HealthAddress,
		ServiceMonitorPeriod: o.ServiceMonitorPeriod,
		MetricsAddress:       o.MetricsAddress,
		ShutdownGracePeriod:  o.ShutdownGracePeriod,
		GRPC:                 completedGRPC,
		Logs:                 o.Logs,
		Storage:              completedStorage,
	}

	return CompletedOptions{
		completedOptions: &completed,
	}, nil
}

//nolint:gocyclo,cyclop
func (o *CompletedOptions) Validate() []error {
	if o == nil {
		return nil
	}

	allErrors := []error{}

	if o.NodeName == "" {
		allErrors = append(allErrors, fmt.Errorf("hostname-override: required"))
	} else {
		if validationErrors := validation.IsDNS1123Subdomain(o.NodeName); len(validationErrors) > 0 {
			for _, errDesc := range validationErrors {
				allErrors = append(allErrors, fmt.Errorf("hostname-override %q: %s", o.NodeName, errDesc))
			}
		}
	}

	if o.HealthAddress == "" {
		allErrors = append(allErrors, fmt.Errorf("health-probe-bind-address: required"))
	} else {
		if validationErrors := nvvalidation.IsTCPAddress(o.HealthAddress); len(validationErrors) > 0 {
			for _, errDesc := range validationErrors {
				allErrors = append(allErrors, fmt.Errorf("health-probe-bind-address %q: %s", o.HealthAddress, errDesc))
			}
		}
	}

	if o.ServiceMonitorPeriod < 0 {
		allErrors = append(allErrors,
			fmt.Errorf("service-monitor-period: %v must be greater than or equal to 0s",
				o.ServiceMonitorPeriod))
	} else if o.ServiceMonitorPeriod > 1*time.Minute {
		allErrors = append(allErrors,
			fmt.Errorf("service-monitor-period: %v must be 1m or less",
				o.ServiceMonitorPeriod))
	}

	if o.MetricsAddress != "" {
		if validationErrors := nvvalidation.IsTCPAddress(o.MetricsAddress); len(validationErrors) > 0 {
			for _, errDesc := range validationErrors {
				allErrors = append(allErrors, fmt.Errorf("metrics-bind-address %q: %s", o.MetricsAddress, errDesc))
			}
		}
	}

	if o.HealthAddress != "" && o.MetricsAddress != "" {
		_, healthPort, _ := net.SplitHostPort(o.HealthAddress)
		_, metricsPort, _ := net.SplitHostPort(o.MetricsAddress)

		if healthPort != "" && healthPort == metricsPort {
			allErrors = append(allErrors,
				fmt.Errorf("health-probe-bind-address and metrics-bind-address: must not use the same port (%s)",
					healthPort))
		}
	}

	if o.ShutdownGracePeriod < 0 {
		allErrors = append(allErrors,
			fmt.Errorf("shutdown-grace-period: %v must be greater than or equal to 0s",
				o.ShutdownGracePeriod))
	} else if o.ShutdownGracePeriod > 10*time.Minute {
		allErrors = append(allErrors,
			fmt.Errorf("shutdown-grace-period: %v must be 10m or less",
				o.ShutdownGracePeriod))
	}

	allErrors = append(allErrors, o.GRPC.Validate()...)
	allErrors = append(allErrors, o.Storage.Validate()...)

	if o.Logs != nil {
		if logErrs := logsapi.Validate(o.Logs, nil, nil); len(logErrs) > 0 {
			allErrors = append(allErrors, logErrs.ToAggregate().Errors()...)
		}
	}

	return allErrors
}
