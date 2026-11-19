//go:build windows

package discover

import (
	"golang.org/x/sys/windows/registry"
)

const (
	tcpPorts = `SYSTEM\CurrentControlSet\Control\Print\Monitors\Standard TCP/IP Port\Ports`
	printers = `SYSTEM\CurrentControlSet\Control\Print\Printers`
)

// windowsPorts returns the raw-printing network addresses Windows already
// has "Standard TCP/IP" ports for, each with the name of the Windows printer
// using it (or the port's name). These may be on a network the scan doesn't
// cover. Read-only; needs no administrator rights.
func windowsPorts() map[string]string {
	out := map[string]string{}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, tcpPorts, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return out
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	byPort := map[string]string{}
	for _, name := range names {
		pk, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		proto, _, _ := pk.GetIntegerValue("Protocol") // 1 raw, 2 LPR
		host, _, _ := pk.GetStringValue("IPAddress")
		if host == "" {
			host, _, _ = pk.GetStringValue("HostName")
		}
		port, _, _ := pk.GetIntegerValue("PortNumber")
		pk.Close()
		if proto != 1 {
			continue
		}
		if addr, ok := joinPort(host, port); ok {
			byPort[name] = addr
			out[addr] = name
		}
	}
	// Name each address after the Windows printer that prints to it.
	pk, err := registry.OpenKey(registry.LOCAL_MACHINE, printers, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return out
	}
	defer pk.Close()
	pnames, _ := pk.ReadSubKeyNames(-1)
	for _, pn := range pnames {
		k, err := registry.OpenKey(pk, pn, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		port, _, _ := k.GetStringValue("Port")
		k.Close()
		if addr, ok := byPort[port]; ok {
			out[addr] = pn
		}
	}
	return out
}
