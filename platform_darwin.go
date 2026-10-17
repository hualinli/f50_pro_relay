//go:build darwin

package relay

import "errors"

func bindInterface(fd uintptr) error { return errors.New("device network operations require Linux") }
func chownWifi(path string) error    { return errors.New("device network operations require Linux") }
func purgeClientConnections(oldIP string, lan LAN) (int, error) {
	return 0, errors.New("device network operations require Linux")
}
