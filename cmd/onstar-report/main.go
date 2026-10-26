package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstarreport"
	"github.com/JeremyProffittOrg/suburban-remote/internal/ownerclient"
	"os"
	"path/filepath"
	"time"
)

func main() {
	store, e := ownerclient.DefaultStore()
	if e != nil {
		fail()
	}
	if _, e = store.Load(); e != nil {
		fmt.Fprintln(os.Stderr, "Owner sign-in required. Run scripts/owner-login.bat first.")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	report := onstarreport.Build(ctx, ownerclient.New(store))
	if os.MkdirAll("dist", 0700) != nil {
		fail()
	}
	path := filepath.Join("dist", "onstar-report.html")
	file, e := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		fail()
	}
	e = onstarreport.Render(file, report)
	closeErr := file.Close()
	if e != nil || closeErr != nil {
		fail()
	}
	metadata, e := json.MarshalIndent(report.Metadata, "", "  ")
	if e != nil || os.WriteFile(filepath.Join("dist", "onstar-fields.json"), metadata, 0600) != nil {
		fail()
	}
	fmt.Printf("Report saved: %s; real redacted exchanges: %d\n", path, report.Captured)
	if report.Captured == 0 {
		os.Exit(2)
	}
}
func fail() {
	fmt.Fprintln(os.Stderr, "Report export unavailable; no private values are shown.")
	os.Exit(1)
}
