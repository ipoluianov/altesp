package serial

// holder: Windows doesn't tell who holds a COM port without a driver of its
// own
func holder(name string) string { return "" }
