package notify

import "github.com/godbus/dbus/v5"

// show calls org.freedesktop.Notifications (every desktop has a server).
func show(title, body string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.Notify", 0,
		"used", uint32(0), "computer", title, body, []string{}, map[string]dbus.Variant{}, int32(-1)).Err
}
