// Copyright © 2020 The Tekton Authors.
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

package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"

	"go.uber.org/zap"

	category "github.com/tektoncd/hub/api/gen/category"
	resource "github.com/tektoncd/hub/api/gen/resource"
	status "github.com/tektoncd/hub/api/gen/status"
	"github.com/tektoncd/hub/api/pkg/app"
	categorysvc "github.com/tektoncd/hub/api/pkg/service/category"
	resourcesvc "github.com/tektoncd/hub/api/pkg/service/resource"
	statussvc "github.com/tektoncd/hub/api/pkg/service/status"
)

func main() {
	// Define command line flags, add any other flag required to configure the
	// service.
	var (
		hostF     = flag.String("host", "localhost", "Server host (valid values: localhost)")
		domainF   = flag.String("domain", "", "Host domain name (overrides host domain specified in service design)")
		httpPortF = flag.String("http-port", "", "HTTP port (overrides host HTTP port specified in service design)")
		secureF   = flag.Bool("secure", false, "Use secure scheme (https or grpcs)")
		dbgF      = flag.Bool("debug", false, "Log request and response bodies")
	)
	flag.Parse()

	var (
		api    app.Config
		logger *zap.SugaredLogger
		err    error
	)
	{
		api, err = app.FromEnv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: failed to initialise: %s", err)
			os.Exit(1)
		}
		api.DB().LogMode(true)
		logger = api.Logger()
		defer api.Cleanup()
	}

	// Initialize the services.
	var (
		categorySvc category.Service
		resourceSvc resource.Service
		statusSvc   status.Service
	)
	{
		categorySvc = categorysvc.New(api)
		resourceSvc = resourcesvc.New(api)
		statusSvc = statussvc.New()
	}

	// Wrap the services in endpoints that can be invoked from other services
	// potentially running in different processes.
	var (
		categoryEndpoints *category.Endpoints
		resourceEndpoints *resource.Endpoints
		statusEndpoints   *status.Endpoints
	)
	{
		categoryEndpoints = category.NewEndpoints(categorySvc)
		resourceEndpoints = resource.NewEndpoints(resourceSvc)
		statusEndpoints = status.NewEndpoints(statusSvc)
	}

	// Create channel used by both the signal handler and server goroutines
	// to notify the main goroutine when to stop the server.
	errc := make(chan error)

	// Setup interrupt handler. This optional step configures the process so
	// that SIGINT and SIGTERM signals cause the services to stop gracefully.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		errc <- fmt.Errorf("%s", <-c)
	}()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Start the servers and send errors (if any) to the error channel.
	switch *hostF {
	case "localhost":
		{
			addr := "http://:8000"
			u, err := url.Parse(addr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid URL %#v: %s\n", addr, err)
				os.Exit(1)
			}
			if *secureF {
				u.Scheme = "https"
			}
			if *domainF != "" {
				u.Host = *domainF
			}
			if *httpPortF != "" {
				h := strings.Split(u.Host, ":")[0]
				u.Host = h + ":" + *httpPortF
			} else if u.Port() == "" {
				u.Host += ":80"
			}
			handleHTTPServer(
				ctx, u, &wg, errc, *dbgF,
				categoryEndpoints, resourceEndpoints, statusEndpoints,
				logger,
			)
		}

	default:
		fmt.Fprintf(os.Stderr, "invalid host argument: %q (valid hosts: localhost)\n", *hostF)
	}

	// Wait for signal.
	logger.Infof("exiting (%v)", <-errc)

	// Send cancellation signal to the goroutines.
	cancel()

	wg.Wait()
	logger.Info("exited")
}
