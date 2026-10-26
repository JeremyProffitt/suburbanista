package main

import (
	"context"
	"fmt"
	"github.com/JeremyProffittOrg/suburban-remote/internal/ownerclient"
	"os"
	"time"
)

func main() {
	store, e := ownerclient.DefaultStore()
	if e == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		present, close, err := handoff(ctx)
		if err != nil {
			e = err
		} else {
			defer close()
			e = ownerclient.New(store).Login(ctx, present)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "Owner sign-in did not complete. No credentials are shown.")
		os.Exit(1)
	}
	fmt.Println("Owner session protected locally. Report exporter is ready. Remote MCP uses its own browser OAuth connection.")
}
