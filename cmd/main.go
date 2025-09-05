package main

import (
	"context"
	"flag"
	"github.com/argoproj-labs/argocd-metric-ext-server/internal/logging"
	"github.com/argoproj-labs/argocd-metric-ext-server/internal/server"
)

func main() {
	var port int
	var enableTLS bool
	var skipPrometheusTLSVerify bool
	flag.IntVar(&port, "port", 9003, "Listening Port")
	flag.BoolVar(&enableTLS, "enableTLS", true, "Run server with TLS (default true)")
	flag.BoolVar(&skipPrometheusTLSVerify, "skipPrometheusTLSVerify", false, "Skip TLS certificate verification when connecting to Prometheus (default false)")
	flag.Parse()
	logger := logging.NewLogger().Named("metric-sever")
	ctx := context.Background()
	defer ctx.Done()

	metricsServer := server.NewO11yServer(logger, port, enableTLS, skipPrometheusTLSVerify)
	metricsServer.Run(ctx)
}
