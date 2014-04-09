package net

import (
	"bytes"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	bosherr "bosh/errors"
	boshsettings "bosh/settings"
	boshsys "bosh/system"
)

type ubuntu struct {
	arpWaitInterval time.Duration
	cmdRunner       boshsys.CmdRunner
	fs              boshsys.FileSystem
}

func NewUbuntuNetManager(
	fs boshsys.FileSystem,
	cmdRunner boshsys.CmdRunner,
	arpWaitInterval time.Duration,
) (net ubuntu) {
	net.arpWaitInterval = arpWaitInterval
	net.cmdRunner = cmdRunner
	net.fs = fs
	return
}

func (net ubuntu) getDNSServers(networks boshsettings.Networks) []string {
	var dnsServers []string
	dnsNetwork, found := networks.DefaultNetworkFor("dns")
	if found {
		for i := len(dnsNetwork.DNS) - 1; i >= 0; i-- {
			dnsServers = append(dnsServers, dnsNetwork.DNS[i])
		}
	}
	return dnsServers
}

func (net ubuntu) SetupDhcp(networks boshsettings.Networks) error {
	dnsServers := net.getDNSServers(networks)

	buffer := bytes.NewBuffer([]byte{})
	t := template.Must(template.New("dhcp-config").Parse(ubuntuDHCPConfigTemplate))

	err := t.Execute(buffer, dnsConfigArg{dnsServers})
	if err != nil {
		return bosherr.WrapError(err, "Generating config from template")
	}

	written, err := net.fs.ConvergeFileContents("/etc/dhcp3/dhclient.conf", buffer.Bytes())
	if err != nil {
		return bosherr.WrapError(err, "Writing to /etc/dhcp3/dhclient.conf")
	}

	if written {
		// Ignore errors here, just run the commands
		net.cmdRunner.RunCommand("pkill", "dhclient3")
		net.cmdRunner.RunCommand("/etc/init.d/networking", "restart")
	}

	return nil
}

// DHCP Config file - /etc/dhcp3/dhclient.conf
const ubuntuDHCPConfigTemplate = `# Generated by bosh-agent

option rfc3442-classless-static-routes code 121 = array of unsigned integer 8;

send host-name "<hostname>";

request subnet-mask, broadcast-address, time-offset, routers,
	domain-name, domain-name-servers, domain-search, host-name,
	netbios-name-servers, netbios-scope, interface-mtu,
	rfc3442-classless-static-routes, ntp-servers;

{{ range .DNSServers }}prepend domain-name-servers {{ . }};
{{ end }}`

func (net ubuntu) SetupManualNetworking(networks boshsettings.Networks) error {
	modifiedNetworks, written, err := net.writeNetworkInterfaces(networks)
	if err != nil {
		return bosherr.WrapError(err, "Writing network interfaces")
	}

	if written {
		net.restartNetworkingInterfaces(modifiedNetworks)
	}

	err = net.writeResolvConf(networks)
	if err != nil {
		return bosherr.WrapError(err, "Writing resolv.conf")
	}

	go net.gratuitiousArp(modifiedNetworks)

	return nil
}

func (net ubuntu) gratuitiousArp(networks []CustomNetwork) {
	for i := 0; i < 6; i++ {
		for _, network := range networks {
			for !net.fs.FileExists(filepath.Join("/sys/class/net", network.Interface)) {
				time.Sleep(100 * time.Millisecond)
			}

			net.cmdRunner.RunCommand("arping", "-c", "1", "-U", "-I", network.Interface, network.IP)
			time.Sleep(net.arpWaitInterval)
		}
	}
}

func (net ubuntu) writeNetworkInterfaces(networks boshsettings.Networks) ([]CustomNetwork, bool, error) {
	var modifiedNetworks []CustomNetwork

	macAddresses, err := net.detectMacAddresses()
	if err != nil {
		return modifiedNetworks, false, bosherr.WrapError(err, "Detecting mac addresses")
	}

	for _, aNet := range networks {
		network, broadcast, err := boshsys.CalculateNetworkAndBroadcast(aNet.IP, aNet.Netmask)
		if err != nil {
			return modifiedNetworks, false, bosherr.WrapError(err, "Calculating network and broadcast")
		}

		newNet := CustomNetwork{
			aNet,
			macAddresses[aNet.Mac],
			network,
			broadcast,
			true,
		}
		modifiedNetworks = append(modifiedNetworks, newNet)
	}

	buffer := bytes.NewBuffer([]byte{})
	t := template.Must(template.New("network-interfaces").Parse(ubuntuNetworkInterfacesTemplate))

	err = t.Execute(buffer, modifiedNetworks)
	if err != nil {
		return modifiedNetworks, false, bosherr.WrapError(err, "Generating config from template")
	}

	written, err := net.fs.ConvergeFileContents("/etc/network/interfaces", buffer.Bytes())
	if err != nil {
		return modifiedNetworks, false, bosherr.WrapError(err, "Writing to /etc/network/interfaces")
	}

	return modifiedNetworks, written, nil
}

const ubuntuNetworkInterfacesTemplate = `# Generated by bosh-agent
auto lo
iface lo inet loopback
{{ range . }}
auto {{ .Interface }}
iface {{ .Interface }} inet static
    address {{ .IP }}
    network {{ .NetworkIP }}
    netmask {{ .Netmask }}
    broadcast {{ .Broadcast }}
{{ if .HasDefaultGateway }}    gateway {{ .Gateway }}{{ end }}{{ end }}`

func (net ubuntu) writeResolvConf(networks boshsettings.Networks) error {
	buffer := bytes.NewBuffer([]byte{})
	t := template.Must(template.New("resolv-conf").Parse(ubuntuResolvConfTemplate))

	dnsServers := net.getDNSServers(networks)
	dnsServersArg := dnsConfigArg{dnsServers}
	err := t.Execute(buffer, dnsServersArg)
	if err != nil {
		return bosherr.WrapError(err, "Generating config from template")
	}

	err = net.fs.WriteFile("/etc/resolv.conf", buffer.Bytes())
	if err != nil {
		return bosherr.WrapError(err, "Writing to /etc/resolv.conf")
	}

	return nil
}

const ubuntuResolvConfTemplate = `# Generated by bosh-agent
{{ range .DNSServers }}nameserver {{ . }}
{{ end }}`

func (net ubuntu) detectMacAddresses() (map[string]string, error) {
	addresses := map[string]string{}

	filePaths, err := net.fs.Glob("/sys/class/net/*")
	if err != nil {
		return addresses, bosherr.WrapError(err, "Getting file list from /sys/class/net")
	}

	var macAddress string
	for _, filePath := range filePaths {
		macAddress, err = net.fs.ReadFileString(filepath.Join(filePath, "address"))
		if err != nil {
			return addresses, bosherr.WrapError(err, "Reading mac address from file")
		}

		macAddress = strings.Trim(macAddress, "\n")

		interfaceName := filepath.Base(filePath)
		addresses[macAddress] = interfaceName
	}

	return addresses, nil
}

func (net ubuntu) restartNetworkingInterfaces(networks []CustomNetwork) {
	for _, network := range networks {
		net.cmdRunner.RunCommand("service", "network-interface", "stop", "INTERFACE="+network.Interface)
		net.cmdRunner.RunCommand("service", "network-interface", "start", "INTERFACE="+network.Interface)
	}
}
