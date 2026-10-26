package main

import (
	"bytes"
	"testing"
)

func TestOnStarCLIRequiresMainCIAndLiveFlag(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	var output bytes.Buffer
	if code := runOnStar([]string{"--mode", "deployed", "--live"}, &output, &output); code != 2 {
		t.Fatal("local invocation was accepted")
	}
}
