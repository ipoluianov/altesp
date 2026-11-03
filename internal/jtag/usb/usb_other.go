//go:build !linux && !windows && !darwin

package usb

func list() ([]DeviceInfo, error) { return nil, newErr("list", ErrUnsupported, nil, "") }

func open(d DeviceInfo, m Match) (*Device, error) {
	return nil, newErr("open", ErrUnsupported, nil, "")
}
