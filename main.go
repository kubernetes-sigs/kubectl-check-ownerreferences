/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"sigs.k8s.io/kubectl-check-ownerreferences/pkg"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/metadata"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
)

func checkErr(err error) {
	if err != nil {
		klog.Error(err.Error())
		os.Exit(1)
	}
}

func main() {
	version := false
	flag.BoolVar(&version, "version", version, "display version information")

	output := ""
	burst := 100
	qps := 25
	pflag.StringVarP(&output, "output", "o", output, "Output format. May be '' or 'json'.")
	pflag.IntVar(&burst, "burst", burst, "API requests allowed per second (burst).")
	pflag.IntVar(&qps, "qps", qps, "API requests allowed per second (steady state). Set to -1 to disable rate limiter.")

	// set up logging
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	// set up config flags
	configFlags := genericclioptions.NewConfigFlags(false)
	configFlags.AddFlags(pflag.CommandLine)

	// parse flags
	pflag.Parse()

	if version {
		fmt.Printf("kubectl-check-ownerreferences version %s (built with %v)\n", pkg.Version, pkg.GoVersion)
		os.Exit(0)
	}

	if burst <= 0 {
		klog.Fatalf("invalid burst rate, must be > 0")
	}
	if qps < -1 {
		klog.Fatalf("invalid qps, must be >= 0")
	}

	// set up REST config
	config, err := configFlags.ToRESTConfig()
	if err != nil && (strings.Contains(err.Error(), "incomplete configuration") || strings.Contains(err.Error(), "no configuration")) {
		// try falling back to in-cluster config
		klog.Warningf("attempting to use in-cluster config, error loading client config: %v", err)
		config, err = rest.InClusterConfig()
	}
	checkErr(err)
	// raise burst/qps
	config.Burst = burst
	config.QPS = float32(qps)
	// silence deprecation warnings, we're iterating over all types
	config.WarningHandler = rest.NoWarnings{}
	// prefer protobuf for efficiency
	config.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"

	// set up clients
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	checkErr(err)
	metadataClient, err := metadata.NewForConfig(config)
	checkErr(err)

	opts := &pkg.VerifyGCOptions{
		DiscoveryClient: discoveryClient,
		MetadataClient:  metadataClient,
		Output:          output,
		Stderr:          os.Stderr,
		Stdout:          os.Stdout,
	}
	checkErr(opts.Validate())
	checkErr(opts.Run())
}
