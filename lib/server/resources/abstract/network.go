/*
 * Copyright 2018-2020, CS Systemes d'Information, http://www.c-s.fr
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package abstract

import (
    "encoding/json"

    "github.com/sirupsen/logrus"

    "github.com/CS-SI/SafeScale/lib/server/resources/enums/ipversion"
    "github.com/CS-SI/SafeScale/lib/server/resources/enums/networkstate"
    "github.com/CS-SI/SafeScale/lib/utils/data"
    "github.com/CS-SI/SafeScale/lib/utils/fail"
)

// NetworkRequest represents network requirements to create a subnet where Mask is defined in CIDR notation
// like "192.0.2.0/24" or "2001:db8::/32", as defined in RFC 4632 and RFC 4291.
type NetworkRequest struct {
    Name string
    // IPVersion must be IPv4 or IPv6 (see IPVersion)
    IPVersion ipversion.Enum
    // CIDR mask
    CIDR string
    // Domain contains the DNS suffix to use for this network
    Domain string
    // DNSServers
    DNSServers []string
    // HA tells if 2 gateways and a VIP needs to be created; the VIP IP address will be used as gateway
    HA bool
    // Image ontains the string of the image requested for gateway(s)
    Image string
    // KeepOnFailure tells if resources have to be kept in case of failure (default behavior is to delete them)
    KeepOnFailure bool
}

// FIXME: comment!
type SubNetwork struct {
    CIDR string `json:"subnetmask,omitempty"` // FIXME: comment!
    ID   string `json:"subnetid,omitempty"`   // FIXME: comment!
}

// Network represents a virtual network
type Network struct {
    ID                 string            `json:"id,omitempty"`                   // GetID for the network (from provider)
    Name               string            `json:"name,omitempty"`                 // GetName of the network
    CIDR               string            `json:"mask,omitempty"`                 // network in CIDR notation
    Domain             string            `json:"domain,omitempty"`               // contains the domain used to define host FQDN
    GatewayID          string            `json:"gateway_id,omitempty"`           // contains the id of the host acting as primary gateway for the network
    SecondaryGatewayID string            `json:"secondary_gateway_id,omitempty"` // contains the id of the host acting as secondary gateway for the network
    VIP                *VirtualIP        `json:"vip,omitempty"`                  // contains the VIP of the network if created with HA
    IPVersion          ipversion.Enum    `json:"ip_version,omitempty"`           // IPVersion is IPv4 or IPv6 (see IPVersion)
    NetworkState       networkstate.Enum `json:"status,omitempty"`

    Subnetworks []SubNetwork `json:"subnetworks,omitempty"` // FIXME: comment!

    Subnet bool   // FIXME: comment!
    Parent string // FIXME: comment!
}

// NewNetwork initializes a new instance of Network
func NewNetwork() *Network {
    return &Network{
        NetworkState: networkstate.UNKNOWNSTATE,
    }
}

// Clone ...
// satisfies interface data.Clonable
func (n *Network) Clone() data.Clonable {
    return NewNetwork().Replace(n)
}

// Replace ...
// satisfies interface data.Clonable
func (n *Network) Replace(p data.Clonable) data.Clonable {
    *n = *p.(*Network)
    return n
}

// OK ...
func (n *Network) OK() bool {
    result := n != nil

    result = result && (n.ID != "")
    if n.ID == "" {
        logrus.Debug("Network without GetID")
    }
    result = result && (n.Name != "")
    if n.Name == "" {
        logrus.Debug("Network without name")
    }
    result = result && (n.CIDR != "")
    if n.CIDR == "" {
        logrus.Debug("Network without CIDR")
    }
    result = result && (n.GatewayID != "")
    if n.GatewayID == "" {
        logrus.Debug("Network without getGateway")
    }

    return result
}

// Serialize serializes Host instance into bytes (output json code)
func (n *Network) Serialize() ([]byte, fail.Error) {
    if n == nil {
        return nil, fail.InvalidInstanceError()
    }
    r, err := json.Marshal(n)
    return r, fail.ToError(err)
}

// Deserialize reads json code and reinstantiates an Host
func (n *Network) Deserialize(buf []byte) (xerr fail.Error) {
    if n == nil {
        return fail.InvalidInstanceError()
    }
    defer fail.OnPanic(&xerr) // json.Unmarshal may panic
    return fail.ToError(json.Unmarshal(buf, n))
}

// GetName ...
// satisfies interface data.Identifiable
func (n *Network) GetName() string {
    if n == nil {
        return ""
    }
    return n.Name
}

// GetID ...
// satisfies interface data.Identifiable
func (n *Network) GetID() string {
    if n == nil {
        return ""
    }
    return n.ID
}

// VirtualIP is a structure containing information needed to manage VIP (virtual IP)
type VirtualIP struct {
    ID        string      `json:"id,omitempty"`
    Name      string      `json:"name,omitempty"`
    NetworkID string      `json:"network_id,omitempty"`
    PrivateIP string      `json:"private_ip,omitempty"`
    PublicIP  string      `json:"public_ip,omitempty"`
    Hosts     []*HostCore `json:"hosts,omitempty"`
}

func NewVirtualIP() *VirtualIP {
    return &VirtualIP{Hosts: []*HostCore{}}
}

// Clone ...
// satisfies interface data.Clonable
func (vip *VirtualIP) Clone() data.Clonable {
    return NewVirtualIP().Replace(vip)
}

// Replace ...
// satisfies interface data.Clonable interface
func (vip *VirtualIP) Replace(p data.Clonable) data.Clonable {
    src := p.(*VirtualIP)
    *vip = *src
    vip.Hosts = make([]*HostCore, 0, len(src.Hosts))
    for _, v := range src.Hosts {
        vip.Hosts = append(vip.Hosts, v.Clone().(*HostCore))
    }
    return vip
}