package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

var clientIPHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "True-Client-IP", "CF-Connecting-IP"}

type Identity struct {
	IP          netip.Addr
	Local       bool
	TrustedPeer bool
	Forwarded   bool
}

// ResolveIdentity never rewrites RemoteAddr. All forwarded identity families
// must agree, and a chain can only cross a hop explicitly trusted by the operator.
func ResolveIdentity(r *http.Request, trusted []netip.Prefix) Identity {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return Identity{}
	}
	ip := peer.Addr().Unmap()
	id := Identity{IP: ip, TrustedPeer: containsIP(trusted, ip)}
	for _, header := range clientIPHeaders {
		for _, value := range r.Header.Values(header) {
			id.Forwarded = id.Forwarded || strings.TrimSpace(value) != ""
		}
	}
	if !id.Forwarded {
		id.Local = ip.IsLoopback() && localHost(r.Host)
		return id
	}
	if !id.TrustedPeer {
		return id
	}
	var consensus netip.Addr
	for _, name := range clientIPHeaders {
		values := r.Header.Values(name)
		populated := false
		for _, value := range values {
			populated = populated || strings.TrimSpace(value) != ""
		}
		if !populated {
			continue
		}
		value := strings.Join(values, ",")
		var chain []netip.Addr
		switch name {
		case "X-Forwarded-For":
			chain, err = parseXFF(value)
		case "Forwarded":
			chain, err = parseForwarded(value)
		default:
			var parsed netip.Addr
			parsed, err = netip.ParseAddr(strings.TrimSpace(value))
			if len(values) != 1 || parsed.Zone() != "" {
				err = errors.New("invalid singleton IP header")
			}
			chain = []netip.Addr{parsed.Unmap()}
		}
		if err != nil || len(chain) == 0 {
			id.IP = netip.Addr{}
			return id
		}
		resolved := ip
		for i := len(chain) - 1; i >= 0 && containsIP(trusted, resolved); i-- {
			resolved = chain[i]
		}
		if consensus.IsValid() && resolved != consensus {
			id.IP = netip.Addr{}
			return id
		}
		consensus = resolved
	}
	id.IP = consensus
	id.Local = consensus.IsLoopback() && localHost(r.Host)
	return id
}

func containsIP(networks []netip.Prefix, ip netip.Addr) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func localHost(host string) bool {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

func parseXFF(value string) ([]netip.Addr, error) {
	if len(value) > 8192 {
		return nil, errors.New("forwarded chain too large")
	}
	var result []netip.Addr
	for _, part := range strings.Split(value, ",") {
		ip, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil || ip.Zone() != "" {
			return nil, errors.New("invalid forwarded IP")
		}
		result = append(result, ip.Unmap())
	}
	return result, nil
}

func parseForwarded(value string) ([]netip.Addr, error) {
	if len(value) > 8192 {
		return nil, errors.New("forwarded chain too large")
	}
	var result []netip.Addr
	for value != "" {
		seen := map[string]bool{}
		var ip netip.Addr
		for {
			value = strings.TrimLeft(value, " \t")
			key, rest, found := strings.Cut(value, "=")
			if !found || !httpToken(key) || seen[strings.ToLower(key)] {
				return nil, errors.New("invalid forwarded parameter")
			}
			key = strings.ToLower(key)
			seen[key] = true
			parsed, remaining, quoted, ok := forwardedValue(rest)
			if !ok {
				return nil, errors.New("invalid forwarded value")
			}
			if key == "for" {
				var err error
				ip, err = forwardedNode(parsed, quoted)
				if err != nil {
					return nil, err
				}
			}
			value = strings.TrimLeft(remaining, " \t")
			if value == "" || value[0] == ',' {
				if !ip.IsValid() {
					return nil, errors.New("missing forwarded IP")
				}
				result = append(result, ip.Unmap())
				if value != "" {
					value = strings.TrimLeft(value[1:], " \t")
					if value == "" {
						return nil, errors.New("empty forwarded element")
					}
				}
				break
			}
			if value[0] != ';' {
				return nil, errors.New("invalid forwarded delimiter")
			}
			value = value[1:]
		}
	}
	return result, nil
}

func forwardedValue(value string) (string, string, bool, bool) {
	if value == "" {
		return "", "", false, false
	}
	if value[0] != '"' {
		i := strings.IndexAny(value, ";, \t")
		if i < 0 {
			i = len(value)
		}
		return value[:i], value[i:], false, httpToken(value[:i])
	}
	var parsed strings.Builder
	for i := 1; i < len(value); i++ {
		char := value[i]
		if char == '"' {
			return parsed.String(), value[i+1:], true, true
		}
		if char == '\\' {
			i++
			if i == len(value) {
				break
			}
			char = value[i]
		}
		if char < 32 || char > 126 {
			return "", "", true, false
		}
		parsed.WriteByte(char)
	}
	return "", "", true, false
}

func httpToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}

func forwardedNode(value string, quoted bool) (netip.Addr, error) {
	ip, err := netip.ParseAddr(value)
	if err == nil && ip.Is4() {
		return ip, nil
	}
	if !quoted {
		return netip.Addr{}, errors.New("forwarded port and IPv6 nodes must be quoted")
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		ip, err = netip.ParseAddr(value[1 : len(value)-1])
		if err == nil && ip.Is6() && ip.Zone() == "" {
			return ip, nil
		}
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || len(port) == 0 || len(port) > 5 {
		return netip.Addr{}, errors.New("invalid forwarded node")
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return netip.Addr{}, errors.New("invalid forwarded port")
		}
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return netip.Addr{}, errors.New("invalid forwarded port")
	}
	ip, err = netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, errors.New("invalid forwarded IP")
	}
	return ip, nil
}
