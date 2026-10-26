//go:build !windows

package main

import "errors"

func readAmazonInput() ([]byte, error) {
	return nil, errors.New("attended Amazon enrollment requires the Windows batch entrypoint")
}
