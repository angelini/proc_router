package main

import (
	"context"
	"flag"
	std_log "log"

	"github.com/angelini/proc_router/pkg/router"
	"go.uber.org/zap"
)

type cliArgs struct {
	port   int
	host   string
	script string
}

func parseArgs() *cliArgs {
	port := flag.Int("port", 5251, "Server's port (default: 5251)")
	host := flag.String("host", "127.0.0.1", "Script server hostname")
	script := flag.String("script", "", "Script to execute")

	flag.Parse()

	return &cliArgs{
		port:   *port,
		host:   *host,
		script: *script,
	}
}

func main() {
	ctx := context.Background()
	log, err := zap.NewDevelopment()
	if err != nil {
		std_log.Fatalf("Error building zap %v", err)
	}

	args := parseArgs()

	manager := router.NewManager(ctx, log, args.host, "node", args.script, 30000)
	defer manager.Close()

	err = router.StartServer(ctx, log, manager, args.port, args.script)
	if err != nil {
		log.Fatal("server error", zap.Error(err))
	}
}
