package verify

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

var sourceCoordinates = regexp.MustCompile(`(?i)\b(?:latitude|longitude|lat|lon|lng)[\\"\s:=]+([-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[-+]?\d+)?)`)
var sourceBearer = regexp.MustCompile("(?i)\\bBearer\\s+[^\\s\"`\\\\]+")

// CheckSourcePrivacy scans source and fixture bytes without returning matched data.
// Only the exact invented VINs and zero coordinates in testdata are exempt.
func CheckSourcePrivacy(data []byte, canaries []string) error {
	for _, pattern := range []*regexp.Regexp{sensitivePatterns[0], sourceBearer, sensitivePatterns[3]} {
		if pattern.Match(data) {
			return errors.New("prohibited authentication material")
		}
	}
	for _, vin := range sensitivePatterns[2].FindAllString(string(data), -1) {
		if vin != testdata.VIN && vin != testdata.OtherVIN {
			return errors.New("non-synthetic vehicle identifier")
		}
	}
	for _, match := range sourceCoordinates.FindAllSubmatch(data, -1) {
		value, err := strconv.ParseFloat(string(match[1]), 64)
		if err != nil || value != testdata.Latitude {
			return errors.New("non-synthetic coordinate")
		}
	}
	// Reuse the existing encoded-canary check without source-sensitive patterns.
	for _, value := range canaries {
		if value != "" && canaryContains(data, value) {
			return errors.New("canary material")
		}
	}
	return nil
}

// ScanSourcePrivacy rejects unreadable files and links instead of silently skipping them.
func ScanSourcePrivacy(paths []string, canaries []string) (int, error) {
	if len(paths) == 0 {
		return 0, errors.New("no scan paths")
	}
	count := 0
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errors.New("scan input unavailable")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("scan input is a symbolic link")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
				return errors.New("scan input unsupported")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return errors.New("scan input unreadable")
			}
			if err := CheckSourcePrivacy(data, canaries); err != nil {
				return fmt.Errorf("file-id %x: %w", sha256.Sum256([]byte(path)), err)
			}
			count++
			return nil
		})
		if err != nil {
			return count, err
		}
	}
	if count == 0 {
		return 0, errors.New("scan input empty")
	}
	return count, nil
}
