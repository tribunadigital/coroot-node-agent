package common

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/coroot/coroot-node-agent/flags"
	"inet.af/netaddr"
	"k8s.io/klog/v2"
)

var (
	ConnectionFilter = connectionFilter{
		whitelist: map[string]netaddr.IPPrefix{},
	}
	PortFilter *portFilter
)

func init() {
	klog.Infoln("whitelisted public IPs:", *flags.ExternalNetworksWhitelist)
	for _, prefix := range *flags.ExternalNetworksWhitelist {
		if prefix == "" {
			continue
		}
		p, err := netaddr.ParseIPPrefix(prefix)
		if err != nil {
			klog.Fatalf("invalid network %s: %s", prefix, err)
		}
		ConnectionFilter.WhitelistPrefix(p)
	}
	if r := flags.EphemeralPortRange; r != nil && *r != "" {
		klog.Infoln("ephemeral-port-range:", *r)
		parts := strings.Split(*r, "-")
		if len(parts) != 2 {
			klog.Fatalf("invalid port range: %s", *r)
		}
		from, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil {
			klog.Fatalf("invalid port range: %s", *r)
		}
		to, err := strconv.ParseUint(parts[1], 10, 16)
		if err != nil {
			klog.Fatalf("invalid port range: %s", *r)
		}
		if from > to {
			klog.Fatalf("invalid port range: %s", *r)
		}
		PortFilter = &portFilter{
			from: uint16(from),
			to:   uint16(to),
		}
	}
}

func IsIpPrivate(ip netaddr.IP) bool {
	if ip.IsPrivate() {
		return true
	}
	if ip.Is4() {
		parts := ip.As4()
		return parts[0] == 100 && parts[1]&0xc0 == 64 // 100.64.0.0/10
	}
	return false
}

type connectionFilter struct {
	whitelist map[string]netaddr.IPPrefix
}

func (f connectionFilter) WhitelistIP(ip netaddr.IP) {
	var bits uint8 = 32
	if ip.Is6() {
		bits = 128
	}
	f.WhitelistPrefix(netaddr.IPPrefixFrom(ip, bits))
}

func (f connectionFilter) WhitelistPrefix(p netaddr.IPPrefix) {
	if _, ok := f.whitelist[p.String()]; ok {
		return
	}
	f.whitelist[p.String()] = p
}

func (f connectionFilter) ShouldBeSkipped(dst, actualDst netaddr.IP) bool {
	if dst.IsLinkLocalUnicast() {
		return true
	}
	if IsIpPrivate(dst) || dst.IsLoopback() {
		return false
	}
	for _, prefix := range f.whitelist {
		if prefix.Contains(dst) {
			return false
		}
	}
	if IsIpPrivate(actualDst) || actualDst.IsLoopback() {
		f.WhitelistIP(dst)
		return false
	}
	for _, prefix := range f.whitelist {
		if prefix.Contains(actualDst) {
			f.WhitelistIP(dst)
			return false
		}
	}
	return true
}

type portFilter struct {
	from uint16
	to   uint16
}

func (f *portFilter) ShouldBeSkipped(port uint16) bool {
	if f == nil {
		return false
	}
	return port >= f.from && port <= f.to
}

type HostPort struct {
	host string
	ip   netaddr.IP
	port uint16
}

func HostPortFromIPPort(ipPort netaddr.IPPort) HostPort {
	return HostPort{ip: ipPort.IP(), port: ipPort.Port()}
}

func HostPortWithEmptyIP(host string, port uint16) HostPort {
	return HostPort{host: host, port: port}
}

func (hp HostPort) String() string {
	if hp.Port() == 0 {
		return ""
	}
	return net.JoinHostPort(hp.Host(), strconv.Itoa(int(hp.port)))
}

func (hp HostPort) IPPort() netaddr.IPPort {
	return netaddr.IPPortFrom(hp.ip, hp.port)
}

func (hp HostPort) Port() uint16 {
	return hp.port
}

func (hp HostPort) IP() netaddr.IP {
	return hp.ip
}

func (hp HostPort) Host() string {
	if !hp.ip.IsZero() {
		return hp.ip.String()
	}
	return hp.host
}

type DestinationKey struct {
	destination       HostPort
	actualDestination HostPort
}

func (dk DestinationKey) Destination() HostPort {
	return dk.destination
}

func (dk DestinationKey) ActualDestination() HostPort {
	return dk.actualDestination
}

func (dk DestinationKey) ActualDestinationIfKnown() HostPort {
	if dk.actualDestination.Port() != 0 {
		return dk.actualDestination
	}
	return dk.destination
}

func (dk DestinationKey) DestinationLabelValue() string {
	return dk.destination.String()
}

func (dk DestinationKey) ActualDestinationLabelValue() string {
	return dk.actualDestination.String()
}

func (dk DestinationKey) String() string {
	return fmt.Sprintf("%s (%s)", dk.Destination(), dk.actualDestination.String())
}

var (
	awsS3FQDN = regexp.MustCompile(`.+s3.*.amazonaws.com`)
)

func NewDestinationKey(dst, actualDst netaddr.IPPort, fqdn string) DestinationKey {
	if awsS3FQDN.MatchString(fqdn) {
		return DestinationKey{
			destination: HostPortWithEmptyIP(fqdn, dst.Port()),
		}
	}
	return DestinationKey{
		destination:       HostPortFromIPPort(dst),
		actualDestination: HostPortFromIPPort(actualDst),
	}
}

var ec2NodeRegex = regexp.MustCompile(`ip-\d+-\d+-\d+-\d+\.ec2`)
var externalDomainWithSuffix = regexp.MustCompile(`(.+\.(com|net|org|io))\..+`)

func NormalizeFQDN(fqdn string, requestType string) string {
	if requestType == "TypePTR" {
		return "IP.in-addr.arpa"
	}
	fqdn = ec2NodeRegex.ReplaceAllLiteralString(fqdn, "IP.ec2")
	fqdn = externalDomainWithSuffix.ReplaceAllString(fqdn, "$1.search_path_suffix")
	return fqdn
}
