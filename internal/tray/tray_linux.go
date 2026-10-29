package tray

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sniIface     = "org.kde.StatusNotifierItem"
	sniPath      = dbus.ObjectPath("/StatusNotifierItem")
	menuIface    = "com.canonical.dbusmenu"
	menuPath     = dbus.ObjectPath("/MenuBar")
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = dbus.ObjectPath("/StatusNotifierWatcher")
	introspectIf = "org.freedesktop.DBus.Introspectable"
)

// D-Bus wire types: a(iiay), (sa(iiay)ss) and the dbusmenu (ia{sv}av) node.
type pixmap struct {
	W, H int32
	Data []byte
}

type tooltip struct {
	Icon    string
	Pixmaps []pixmap
	Title   string
	Body    string
}

type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type groupProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

func pixmaps(img image.Image) []pixmap {
	if img == nil {
		return nil
	}
	w, h, data := ARGB32(img)
	return []pixmap{{int32(w), int32(h), data}}
}

func tip(text string) tooltip { return tooltip{Title: "used", Body: text} }

// item implements org.kde.StatusNotifierItem methods; all are no-ops since
// the host shows our menu itself (ItemIsMenu).
type item struct{}

func (item) Activate(x, y int32) *dbus.Error               { return nil }
func (item) SecondaryActivate(x, y int32) *dbus.Error      { return nil }
func (item) ContextMenu(x, y int32) *dbus.Error            { return nil }
func (item) Scroll(delta int32, orient string) *dbus.Error { return nil }

// menu implements com.canonical.dbusmenu. Item i of the Menu has id i+1.
type menu struct {
	mu      sync.Mutex
	items   []Item
	rev     uint32
	onClick func(string)
}

func (m *menu) props(id int32) map[string]dbus.Variant {
	if id == 0 {
		return map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}
	}
	if id < 1 || int(id) > len(m.items) {
		return nil
	}
	it := m.items[id-1]
	if it.Label == "" {
		return map[string]dbus.Variant{"type": dbus.MakeVariant("separator")}
	}
	p := map[string]dbus.Variant{
		"label":   dbus.MakeVariant(it.Label),
		"enabled": dbus.MakeVariant(!it.Disabled && it.ID != ""),
	}
	if it.Checked {
		p["toggle-type"] = dbus.MakeVariant("checkmark")
		p["toggle-state"] = dbus.MakeVariant(int32(1))
	}
	return p
}

func (m *menu) GetLayout(parent, depth int32, names []string) (uint32, layout, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.props(parent)
	if p == nil {
		return 0, layout{}, dbus.MakeFailedError(fmt.Errorf("no menu item %d", parent))
	}
	l := layout{ID: parent, Props: p, Children: []dbus.Variant{}}
	if parent == 0 && depth != 0 {
		for i := range m.items {
			id := int32(i + 1)
			l.Children = append(l.Children, dbus.MakeVariant(layout{id, m.props(id), []dbus.Variant{}}))
		}
	}
	return m.rev, l, nil
}

func (m *menu) GetGroupProperties(ids []int32, names []string) ([]groupProps, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []groupProps{}
	for _, id := range ids {
		if p := m.props(id); p != nil {
			out = append(out, groupProps{id, p})
		}
	}
	return out, nil
}

func (m *menu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.props(id)[name]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %q on %d", name, id))
	}
	return v, nil
}

func (m *menu) Event(id int32, event string, data dbus.Variant, ts uint32) *dbus.Error {
	if event != "clicked" {
		return nil
	}
	m.mu.Lock()
	var click string
	if id >= 1 && int(id) <= len(m.items) {
		if it := m.items[id-1]; !it.Disabled {
			click = it.ID
		}
	}
	m.mu.Unlock()
	if click != "" {
		go m.onClick(click)
	}
	return nil
}

func (m *menu) EventGroup(events []struct {
	ID    int32
	Event string
	Data  dbus.Variant
	Time  uint32
}) ([]int32, *dbus.Error) {
	for _, e := range events {
		m.Event(e.ID, e.Event, e.Data, e.Time)
	}
	return []int32{}, nil
}

func (m *menu) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (m *menu) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

func run(ctx context.Context, first Menu, updates <-chan Menu, onClick func(id string)) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("tray: session bus: %w", err)
	}
	defer conn.Close()

	m := &menu{items: first.Items, rev: 1, onClick: onClick}
	sniProps := prop.Map{sniIface: {
		"Category":   {Value: "ApplicationStatus", Emit: prop.EmitFalse},
		"Id":         {Value: "used", Emit: prop.EmitFalse},
		"Title":      {Value: "used", Emit: prop.EmitFalse},
		"Status":     {Value: "Active", Emit: prop.EmitFalse},
		"IconName":   {Value: "", Emit: prop.EmitFalse},
		"IconPixmap": {Value: pixmaps(first.Icon), Emit: prop.EmitFalse},
		"ToolTip":    {Value: tip(first.Tooltip), Emit: prop.EmitFalse},
		"ItemIsMenu": {Value: true, Emit: prop.EmitFalse},
		"Menu":       {Value: menuPath, Emit: prop.EmitFalse},
	}}
	menuProps := prop.Map{menuIface: {
		"Version":       {Value: uint32(3), Emit: prop.EmitFalse},
		"TextDirection": {Value: "ltr", Emit: prop.EmitFalse},
		"Status":        {Value: "normal", Emit: prop.EmitFalse},
		"IconThemePath": {Value: []string{}, Emit: prop.EmitFalse},
	}}

	if err := conn.Export(item{}, sniPath, sniIface); err != nil {
		return err
	}
	if err := conn.Export(m, menuPath, menuIface); err != nil {
		return err
	}
	sp, err := prop.Export(conn, sniPath, sniProps)
	if err != nil {
		return err
	}
	if _, err := prop.Export(conn, menuPath, menuProps); err != nil {
		return err
	}
	conn.Export(introspect.Introspectable(sniXML), sniPath, introspectIf)
	conn.Export(introspect.Introspectable(menuXML), menuPath, introspectIf)

	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if _, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
		return fmt.Errorf("tray: request name: %w", err)
	}
	defer conn.ReleaseName(name)

	register := func() error {
		return conn.Object(watcherName, watcherPath).
			Call(watcherName+".RegisterStatusNotifierItem", 0, name).Err
	}
	if err := register(); err != nil {
		var de dbus.Error
		if errors.As(err, &de) && de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
			return errors.New("tray: no StatusNotifierWatcher on the session bus " +
				"(GNOME needs the AppIndicator/KStatusNotifierItem extension)")
		}
		return fmt.Errorf("tray: register with watcher: %w", err)
	}

	// Re-register when the watcher restarts (e.g. plasmashell or waybar reload).
	sigs := make(chan *dbus.Signal, 8)
	conn.Signal(sigs)
	_ = conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case s := <-sigs:
			if s.Name == "org.freedesktop.DBus.NameOwnerChanged" && len(s.Body) == 3 {
				if owner, _ := s.Body[2].(string); owner != "" {
					_ = register()
				}
			}
		case u, ok := <-updates:
			if !ok {
				updates = nil // closed: keep showing the last menu until ctx ends
				continue
			}
			sp.SetMust(sniIface, "IconPixmap", pixmaps(u.Icon))
			sp.SetMust(sniIface, "ToolTip", tip(u.Tooltip))
			conn.Emit(sniPath, sniIface+".NewIcon")
			conn.Emit(sniPath, sniIface+".NewToolTip")

			m.mu.Lock()
			m.items = u.Items
			m.rev++
			rev := m.rev
			m.mu.Unlock()
			conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
		}
	}
}

// Introspection data; some hosts (and dbus tooling) look before calling.
const sniXML = introspect.IntrospectDeclarationString + `<node>
 <interface name="org.kde.StatusNotifierItem">
  <method name="Activate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="SecondaryActivate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="ContextMenu"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="Scroll"><arg name="delta" type="i" direction="in"/><arg name="orientation" type="s" direction="in"/></method>
  <signal name="NewIcon"/>
  <signal name="NewToolTip"/>
  <signal name="NewStatus"><arg name="status" type="s"/></signal>
  <property name="Category" type="s" access="read"/>
  <property name="Id" type="s" access="read"/>
  <property name="Title" type="s" access="read"/>
  <property name="Status" type="s" access="read"/>
  <property name="IconName" type="s" access="read"/>
  <property name="IconPixmap" type="a(iiay)" access="read"/>
  <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
  <property name="ItemIsMenu" type="b" access="read"/>
  <property name="Menu" type="o" access="read"/>
 </interface>` + introspect.IntrospectDataString + prop.IntrospectDataString + `</node>`

const menuXML = introspect.IntrospectDeclarationString + `<node>
 <interface name="com.canonical.dbusmenu">
  <method name="GetLayout">
   <arg name="parentId" type="i" direction="in"/><arg name="recursionDepth" type="i" direction="in"/>
   <arg name="propertyNames" type="as" direction="in"/>
   <arg name="revision" type="u" direction="out"/><arg name="layout" type="(ia{sv}av)" direction="out"/>
  </method>
  <method name="GetGroupProperties">
   <arg name="ids" type="ai" direction="in"/><arg name="propertyNames" type="as" direction="in"/>
   <arg name="properties" type="a(ia{sv})" direction="out"/>
  </method>
  <method name="GetProperty">
   <arg name="id" type="i" direction="in"/><arg name="name" type="s" direction="in"/>
   <arg name="value" type="v" direction="out"/>
  </method>
  <method name="Event">
   <arg name="id" type="i" direction="in"/><arg name="eventId" type="s" direction="in"/>
   <arg name="data" type="v" direction="in"/><arg name="timestamp" type="u" direction="in"/>
  </method>
  <method name="EventGroup">
   <arg name="events" type="a(isvu)" direction="in"/><arg name="idErrors" type="ai" direction="out"/>
  </method>
  <method name="AboutToShow">
   <arg name="id" type="i" direction="in"/><arg name="needUpdate" type="b" direction="out"/>
  </method>
  <method name="AboutToShowGroup">
   <arg name="ids" type="ai" direction="in"/>
   <arg name="updatesNeeded" type="ai" direction="out"/><arg name="idErrors" type="ai" direction="out"/>
  </method>
  <signal name="ItemsPropertiesUpdated"><arg type="a(ia{sv})"/><arg type="a(ias)"/></signal>
  <signal name="LayoutUpdated"><arg name="revision" type="u"/><arg name="parent" type="i"/></signal>
  <signal name="ItemActivationRequested"><arg name="id" type="i"/><arg name="timestamp" type="u"/></signal>
  <property name="Version" type="u" access="read"/>
  <property name="TextDirection" type="s" access="read"/>
  <property name="Status" type="s" access="read"/>
  <property name="IconThemePath" type="as" access="read"/>
 </interface>` + introspect.IntrospectDataString + prop.IntrospectDataString + `</node>`
