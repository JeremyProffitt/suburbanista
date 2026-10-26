package homescene

// BackdropJPEG reuses the embedded road photograph in the appearance picker.
func BackdropJPEG(id string) ([]byte, error) {
	return backgroundFiles.ReadFile("backgrounds/" + id + ".jpg")
}
