/*
 * Copyright 2018, CS Systemes d'Information, http://www.c-s.fr
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

package gcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strconv"
	"time"

	"github.com/CS-SI/SafeScale/lib/server/iaas/resources"
	"github.com/CS-SI/SafeScale/lib/server/iaas/resources/enums/HostProperty"
	"github.com/CS-SI/SafeScale/lib/server/iaas/resources/enums/HostState"
	converters "github.com/CS-SI/SafeScale/lib/server/iaas/resources/properties"
	propsv1 "github.com/CS-SI/SafeScale/lib/server/iaas/resources/properties/v1"
	"github.com/CS-SI/SafeScale/lib/server/iaas/resources/userdata"
	common "github.com/CS-SI/SafeScale/lib/utils"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/davecgh/go-spew/spew"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

//-------------IMAGES---------------------------------------------------------------------------------------------------

// ListImages lists available OS images
func (s *Stack) ListImages() (images []resources.Image, err error) {
	compuService := s.ComputeService

	images = []resources.Image{}

	families := []string{"centos-cloud", "debian-cloud", "rhel-cloud", "ubuntu-os-cloud", "suse-cloud", "rhel-sap-cloud", "suse-sap-cloud"}

	for _, family := range families {
		token := ""
		for paginate := true; paginate; {
			resp, err := compuService.Images.List(family).Filter("deprecated.replacement ne .*images.*").PageToken(token).Do()
			if err != nil {
				logrus.Warnf("Can't list public images for project %q", family)
				break
			}

			for _, image := range resp.Items {
				images = append(images, resources.Image{Name: image.Name, URL: image.SelfLink, ID: strconv.FormatUint(image.Id, 10)})
			}
			token := resp.NextPageToken
			paginate = token != ""
		}
	}

	if len(images) == 0 {
		return images, err
	}

	return images, nil
}

// GetImage returns the Image referenced by id
func (s *Stack) GetImage(id string) (*resources.Image, error) {
	images, err := s.ListImages()
	if err != nil {
		return nil, err
	}

	for _, image := range images {
		if image.ID == id {
			return &image, nil
		}
	}

	return nil, fmt.Errorf("image with id [%s] not found", id)
}

//-------------TEMPLATES------------------------------------------------------------------------------------------------

// ListTemplates overload OpenStackGcp ListTemplate method to filter wind and flex instance and add GPU configuration
func (s *Stack) ListTemplates(all bool) (templates []resources.HostTemplate, err error) {
	compuService := s.ComputeService

	templates = []resources.HostTemplate{}

	token := ""
	for paginate := true; paginate; {
		resp, err := compuService.MachineTypes.List(s.GcpConfig.ProjectId, s.GcpConfig.Zone).PageToken(token).Do()
		if err != nil {
			logrus.Warnf("Can't list public types...: %s", err)
			break
		} else {

			for _, matype := range resp.Items {
				ht := resources.HostTemplate{
					Cores:   int(matype.GuestCpus),
					RAMSize: float32(matype.MemoryMb / 1024),
					//VPL: GCP Template disk sizing is ridiculous at best, so fill it to 0 and let us size the disk ourselves
					//DiskSize: int(matype.ImageSpaceGb),
					DiskSize: 0,
					ID:       strconv.FormatUint(matype.Id, 10),
					Name:     string(matype.Name),
				}
				templates = append(templates, ht)
			}
		}
		token := resp.NextPageToken
		paginate = token != ""
	}

	if len(templates) == 0 {
		return templates, err
	}

	return templates, nil
}

//GetTemplate overload OpenStackGcp GetTemplate method to add GPU configuration
func (s *Stack) GetTemplate(id string) (*resources.HostTemplate, error) {
	templates, err := s.ListTemplates(true)
	if err != nil {
		return nil, err
	}

	for _, template := range templates {
		if template.ID == id {
			return &template, nil
		}
	}

	return nil, fmt.Errorf("template with id [%s] not found", id)
}

//-------------SSH KEYS-------------------------------------------------------------------------------------------------

// CreateKeyPair creates and import a key pair
func (s *Stack) CreateKeyPair(name string) (*resources.KeyPair, error) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	publicKey := privateKey.PublicKey
	pub, _ := ssh.NewPublicKey(&publicKey)
	pubBytes := ssh.MarshalAuthorizedKey(pub)
	pubKey := string(pubBytes)

	priBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	priKeyPem := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: priBytes,
		},
	)
	priKey := string(priKeyPem)
	return &resources.KeyPair{
		ID:         name,
		Name:       name,
		PublicKey:  pubKey,
		PrivateKey: priKey,
	}, nil
}

// GetKeyPair returns the key pair identified by id
func (s *Stack) GetKeyPair(id string) (*resources.KeyPair, error) {
	return nil, fmt.Errorf("Not implemented")
}

// ListKeyPairs lists available key pairs
func (s *Stack) ListKeyPairs() ([]resources.KeyPair, error) {
	return nil, fmt.Errorf("Not implemented")
}

// DeleteKeyPair deletes the key pair identified by id
func (s *Stack) DeleteKeyPair(id string) error {
	return fmt.Errorf("Not implemented")
}

// CreateHost creates an host satisfying request
func (s *Stack) CreateHost(request resources.HostRequest) (host *resources.Host, userData *userdata.Content, err error) {
	userData = userdata.NewContent()

	resourceName := request.ResourceName
	networks := request.Networks
	hostMustHavePublicIP := request.PublicIP

	if networks == nil || len(networks) == 0 {
		return nil, userData, fmt.Errorf("the host %s must be on at least one network (even if public)", resourceName)
	}

	// If no key pair is supplied create one
	if request.KeyPair == nil {
		id, err := uuid.NewV4()
		if err != nil {
			msg := fmt.Sprintf("failed to create host UUID: %+v", err)
			logrus.Debugf(common.Capitalize(msg))
			return nil, userData, fmt.Errorf(msg)
		}

		name := fmt.Sprintf("%s_%s", request.ResourceName, id)
		request.KeyPair, err = s.CreateKeyPair(name)
		if err != nil {
			msg := fmt.Sprintf("failed to create host key pair: %+v", err)
			logrus.Debugf(common.Capitalize(msg))
			return nil, userData, fmt.Errorf(msg)
		}
	}
	if request.Password == "" {
		password, err := common.GeneratePassword(16)
		if err != nil {
			return nil, userData, fmt.Errorf("failed to generate password: %s", err.Error())
		}
		request.Password = password
	}

	// The Default Network is the first of the provided list, by convention
	defaultNetwork := request.Networks[0]
	defaultNetworkID := defaultNetwork.ID
	defaultGateway := request.DefaultGateway
	isGateway := defaultGateway == nil && defaultNetwork.Name != resources.SingleHostNetworkName
	defaultGatewayID := ""
	defaultGatewayPrivateIP := ""
	if defaultGateway != nil {
		err := defaultGateway.Properties.LockForRead(HostProperty.NetworkV1).ThenUse(func(v interface{}) error {
			hostNetworkV1 := v.(*propsv1.HostNetwork)
			defaultGatewayPrivateIP = hostNetworkV1.IPv4Addresses[defaultNetworkID]
			defaultGatewayID = defaultGateway.ID
			return nil
		})
		if err != nil {
			return nil, userData, errors.Wrap(err, "")
		}
	}

	if defaultGateway == nil && !hostMustHavePublicIP {
		return nil, userData, fmt.Errorf("the host %s must have a gateway or be public", resourceName)
	}

	var nets []servers.Network

	// FIXME add provider network to host networks ?

	// Add private networks
	for _, n := range request.Networks {
		nets = append(nets, servers.Network{
			UUID: n.ID,
		})
	}

	// --- prepares data structures for Provider usage ---

	// Constructs userdata content
	err = userData.Prepare(*s.Config, request, defaultNetwork.CIDR, "")
	if err != nil {
		msg := fmt.Sprintf("failed to prepare user data content: %+v", err)
		logrus.Debugf(common.Capitalize(msg))
		return nil, userData, fmt.Errorf(msg)
	}

	// Determine system disk size based on vcpus count
	template, err := s.GetTemplate(request.TemplateID)
	if err != nil {
		return nil, userData, fmt.Errorf("failed to get image: %s", err)
	}
	if request.DiskSize > template.DiskSize {
		template.DiskSize = request.DiskSize
	} else if template.DiskSize == 0 {
		// Determines appropriate disk size
		if template.Cores < 16 {
			template.DiskSize = 100
		} else if template.Cores < 32 {
			template.DiskSize = 200
		} else {
			template.DiskSize = 400
		}
	}

	rim, err := s.GetImage(request.ImageID)
	if err != nil {
		return nil, nil, err
	}

	logrus.Debugf("Selected template: '%s', '%s'", template.ID, template.Name)

	// Select usable availability zone, the first one in the list
	if s.GcpConfig.Zone == "" {
		azList, err := s.ListAvailabilityZones()
		if err != nil {
			return nil, userData, err
		}
		var az string
		for az = range azList {
			break
		}
		s.GcpConfig.Zone = az
		logrus.Debugf("Selected Availability Zone: '%s'", az)
	}

	// Sets provider parameters to create host
	userDataPhase1, err := userData.Generate("phase1")
	if err != nil {
		return nil, userData, err
	}

	// --- Initializes resources.Host ---

	host = resources.NewHost()
	host.PrivateKey = request.KeyPair.PrivateKey // Add PrivateKey to host definition
	host.Password = request.Password

	err = host.Properties.LockForWrite(HostProperty.NetworkV1).ThenUse(func(v interface{}) error {
		hostNetworkV1 := v.(*propsv1.HostNetwork)
		hostNetworkV1.DefaultNetworkID = defaultNetworkID
		hostNetworkV1.DefaultGatewayID = defaultGatewayID
		hostNetworkV1.DefaultGatewayPrivateIP = defaultGatewayPrivateIP
		hostNetworkV1.IsGateway = isGateway
		return nil
	})
	if err != nil {
		return nil, userData, err
	}

	// Adds Host property SizingV1
	err = host.Properties.LockForWrite(HostProperty.SizingV1).ThenUse(func(v interface{}) error {
		hostSizingV1 := v.(*propsv1.HostSizing)
		// Note: from there, no idea what was the RequestedSize; caller will have to complement this information
		hostSizingV1.Template = request.TemplateID
		hostSizingV1.AllocatedSize = converters.ModelHostTemplateToPropertyHostSize(template)
		return nil
	})
	if err != nil {
		return nil, userData, err
	}

	// --- query provider for host creation ---

	logrus.Debugf("requesting host resource creation...")
	var desistError error

	// Retry creation until success, for 10 minutes
	err = retry.WhileUnsuccessfulDelay5Seconds(
		func() error {

			server, err := buildGcpMachine(s.ComputeService, s.GcpConfig.ProjectId, request.ResourceName, rim.URL, s.GcpConfig.Zone, "safescale", defaultNetwork.Name, string(userDataPhase1), isGateway, template)
			if err != nil {
				if server != nil {
					killErr := s.DeleteHost(server.ID)
					if killErr != nil {
						return errors.Wrap(err, killErr.Error())
					}
				}

				if gerr, ok := err.(*googleapi.Error); ok {
					logrus.Warnf("Received GCP errorcode: %d", gerr.Code)
					if gerr.Code == 403 {
						desistError = gerr
						return nil
					}
				}

				logrus.Warnf("error creating host: %+v", err)
				return err
			}

			if server == nil {
				return fmt.Errorf("failed to create server")
			}

			host.ID = server.ID
			host.Name = server.Name

			// Wait that Host is ready, not just that the build is started
			_, err = s.WaitHostReady(host, common.GetLongOperationTimeout())
			if err != nil {
				killErr := s.DeleteHost(host.ID)
				if killErr != nil {
					return errors.Wrap(err, killErr.Error())
				}
				return err
			}
			return nil
		},
		common.GetLongOperationTimeout(),
	)
	if err != nil {
		return nil, userData, errors.Wrap(err, fmt.Sprintf("Error creating host: timeout"))
	}
	if desistError != nil {
		return nil, userData, resources.ResourceAccessDeniedError(request.ResourceName, fmt.Sprintf("Error creating host: %s", desistError.Error()) )
	}

	logrus.Debugf("host resource created.")

	// Starting from here, delete host if exiting with error
	defer func() {
		if err != nil {
			logrus.Infof("Cleanup, deleting host '%s'", host.Name)
			derr := s.DeleteHost(host.ID)
			if derr != nil {
				logrus.Warnf("Error deleting host: %v", derr)
			}
		}
	}()

	if host == nil {
		panic("Unexpected nil host")
	}

	if !host.OK() {
		logrus.Warnf("Missing data in host: %v", host)
	}

	return host, userData, nil
}

// WaitHostReady waits an host achieve ready state
// hostParam can be an ID of host, or an instance of *resources.Host; any other type will panic
func (s *Stack) WaitHostReady(hostParam interface{}, timeout time.Duration) (*resources.Host, error) {
	if s == nil {
		panic("Calling s.WaitHostReady with s==nil!")
	}

	var (
		host *resources.Host
	)
	switch hostParam.(type) {
	case string:
		host = resources.NewHost()
		host.ID = hostParam.(string)
	case *resources.Host:
		host = hostParam.(*resources.Host)
	default:
		panic("hostParam must be a string or a *resources.Host!")
	}
	logrus.Debugf(">>> stacks.gcp::WaitHostReady(%s)", host.ID)
	defer logrus.Debugf("<<< stacks.gcp::WaitHostReady(%s)", host.ID)

	retryErr := retry.WhileUnsuccessful(
		func() error {
			hostTmp, err := s.InspectHost(host)
			if err != nil {
				return err
			}

			host = hostTmp
			if host.LastState != HostState.STARTED {
				return fmt.Errorf("not in ready state (current state: %s)", host.LastState.String())
			}
			return nil
		},
		common.GetDefaultDelay(),
		timeout,
	)
	if retryErr != nil {
		if _, ok := retryErr.(retry.ErrTimeout); ok {
			return host, fmt.Errorf("timeout waiting to get host '%s' information after %v", host.Name, timeout)
		}
		return host, retryErr
	}
	return host, nil
}

func publicAccess(isPublic bool) []*compute.AccessConfig {
	if isPublic {
		return []*compute.AccessConfig{
			{
				Type: "ONE_TO_ONE_NAT",
				Name: "External NAT",
			},
		}
	}

	return []*compute.AccessConfig{}
}

// buildGcpMachine ...
func buildGcpMachine(service *compute.Service, projectID string, instanceName string, imageId string, zone string, network string, subnetwork string, userdata string, isPublic bool, template *resources.HostTemplate) (*resources.Host, error) {
	prefix := "https://www.googleapis.com/compute/v1/projects/" + projectID

	imageURL := imageId

	tag := "nat"
	if !isPublic {
		tag = fmt.Sprintf("no-ip-%s", subnetwork)
	}

	instance := &compute.Instance{
		Name:         instanceName,
		Description:  "compute sample instance",
		MachineType:  prefix + "/zones/" + zone + "/machineTypes/" + template.Name,
		CanIpForward: isPublic,
		Tags: &compute.Tags{
			Items: []string{tag},
		},
		Disks: []*compute.AttachedDisk{
			{
				AutoDelete: true,
				Boot:       true,
				Type:       "PERSISTENT",
				InitializeParams: &compute.AttachedDiskInitializeParams{
					DiskName:    fmt.Sprintf("%s-disk", instanceName),
					SourceImage: imageURL,
					DiskSizeGb:  int64(template.DiskSize),
				},
			},
		},
		NetworkInterfaces: []*compute.NetworkInterface{
			{
				AccessConfigs: publicAccess(isPublic),
				Network:       prefix + "/global/networks/" + network,
				Subnetwork:    prefix + "/regions/europe-west1/subnetworks/" + subnetwork,
			},
		},
		ServiceAccounts: []*compute.ServiceAccount{
			{
				Email: "default",
				Scopes: []string{
					compute.DevstorageFullControlScope,
					compute.ComputeScope,
				},
			},
		},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{
				{
					Key:   "startup-script",
					Value: &userdata,
				},
			},
		},
	}

	op, err := service.Instances.Insert(projectID, zone, instance).Do()
	if err != nil {
		return nil, err
	}

	etag := op.Header.Get("Etag")
	oco := OpContext{
		Operation:    op,
		ProjectId:    projectID,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())
	if err != nil {
		return nil, err
	}

	inst, err := service.Instances.Get(projectID, zone, instanceName).IfNoneMatch(etag).Do()
	if err != nil {
		return nil, err
	}

	logrus.Tracef("Got compute.Instance, err: %#v, %v", inst, err)

	if googleapi.IsNotModified(err) {
		logrus.Warnf("Instance not modified since insert.")
	}

	host := resources.NewHost()
	host.ID = strconv.FormatUint(inst.Id, 10)
	host.Name = inst.Name

	return host, nil
}

// InspectHost returns the host identified by ref (name or id) or by a *resources.Host containing an id
func (s *Stack) InspectHost(hostParam interface{}) (host *resources.Host, err error) {
	switch hostParam.(type) {
	case string:
		host = resources.NewHost()
		host.ID = hostParam.(string)
	case *resources.Host:
		host = hostParam.(*resources.Host)
	default:
		panic("stacks.gcp::InspectHost(): parameter 'hostParam' must be a string or a *resources.Host!")
	}

	if host == nil {
		panic("stacks.gcp::InspectHost(): 'host' not initialized !")
	}

	hostRef := host.Name
	if hostRef == "" {
		hostRef = host.ID
	}

	if common.IsEmpty(host) {
		return nil, resources.ResourceNotFoundError("host", hostRef)
	}

	gcpHost, err := s.ComputeService.Instances.Get(s.GcpConfig.ProjectId, s.GcpConfig.Zone, hostRef).Do()
	if err != nil {
		return nil, err
	}

	host.LastState = stateConvert(gcpHost.Status)
	var subnets []IpInSubnet

	for _, nit := range gcpHost.NetworkInterfaces {
		snet := genUrl(nit.Subnetwork)
		if !common.IsEmpty(snet) {
			pubIp := ""
			for _, aco := range nit.AccessConfigs {
				if aco != nil {
					if aco.NatIP != "" {
						pubIp = aco.NatIP
					}
				}
			}

			subnets = append(subnets, IpInSubnet{
				Subnet:   snet,
				IP:       nit.NetworkIP,
				PublicIP: pubIp,
			})
		}
	}

	var resouceNetworks []IpInSubnet
	for _, sn := range subnets {
		region, err := GetRegionFromSelfLink(sn.Subnet)
		if err != nil {
			continue
		}
		psg, err := s.ComputeService.Subnetworks.Get(s.GcpConfig.ProjectId, region, GetResourceNameFromSelfLink(sn.Subnet)).Do()
		if err != nil {
			continue
		}

		resouceNetworks = append(resouceNetworks, IpInSubnet{
			Subnet:   sn.Subnet,
			Name:     psg.Name,
			ID:       strconv.FormatUint(psg.Id, 10),
			IP:       sn.IP,
			PublicIP: sn.PublicIP,
		})
	}

	ip4bynetid := make(map[string]string)
	netnamebyid := make(map[string]string)
	netidbyname := make(map[string]string)

	ipv4 := ""
	for _, rn := range resouceNetworks {
		ip4bynetid[rn.ID] = rn.IP
		netnamebyid[rn.ID] = rn.Name
		netidbyname[rn.Name] = rn.ID
		if rn.PublicIP != "" {
			ipv4 = rn.PublicIP
		}
	}

	err = host.Properties.LockForWrite(HostProperty.NetworkV1).ThenUse(func(v interface{}) error {
		hostNetworkV1 := v.(*propsv1.HostNetwork)
		hostNetworkV1.IPv4Addresses = ip4bynetid
		hostNetworkV1.IPv6Addresses = make(map[string]string)
		hostNetworkV1.NetworksByID = netnamebyid
		hostNetworkV1.NetworksByName = netidbyname
		if hostNetworkV1.PublicIPv4 == "" {
			hostNetworkV1.PublicIPv4 = ipv4
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Failed to update HostProperty.NetworkV1 : %s", err.Error())
	}

	allocated := fromMachineTypeToAllocatedSize(gcpHost.MachineType)

	err = host.Properties.LockForWrite(HostProperty.SizingV1).ThenUse(func(v interface{}) error {
		hostSizingV1 := v.(*propsv1.HostSizing)
		hostSizingV1.AllocatedSize.Cores = allocated.Cores
		hostSizingV1.AllocatedSize.RAMSize = allocated.RAMSize
		hostSizingV1.AllocatedSize.DiskSize = allocated.DiskSize
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Failed to update HostProperty.SizingV1 : %s", err.Error())
	}

	if !host.OK() {
		logrus.Debugf("[TRACE] Unexpected host status: %s", spew.Sdump(host))
	}

	return host, nil
}

func fromMachineTypeToAllocatedSize(machineType string) propsv1.HostSize {
	hz := propsv1.HostSize{}

	// FIXME Implement mapping

	return hz
}

func stateConvert(gcpHostStatus string) HostState.Enum {
	switch gcpHostStatus {
	case "PROVISIONING":
		return HostState.STARTING
	case "REPAIRING":
		return HostState.ERROR
	case "RUNNING":
		return HostState.STARTED
	case "STAGING":
		return HostState.STARTING
	case "STOPPED":
		return HostState.STOPPED
	case "STOPPING":
		return HostState.STOPPING
	case "SUSPENDED":
		return HostState.STOPPED
	case "SUSPENDING":
		return HostState.STOPPING
	case "TERMINATED":
		return HostState.STOPPED
	default:
		panic(fmt.Sprintf("Unexpected host status: [%s]", gcpHostStatus))
	}
}

// GetHostByName returns the host identified by ref (name or id)
func (s *Stack) GetHostByName(name string) (*resources.Host, error) {
	if name == "" {
		panic("name is empty!")
	}

	hosts, err := s.ListHosts()
	if err != nil {
		return nil, err
	}

	for _, host := range hosts {
		if host.Name == name {
			return host, nil
		}
	}

	return nil, resources.ResourceNotFoundError("host", name)
}

// DeleteHost deletes the host identified by id
func (s *Stack) DeleteHost(id string) (err error) {
	service := s.ComputeService
	projectID := s.GcpConfig.ProjectId
	zone := s.GcpConfig.Zone
	instanceName := id

	_, err = service.Instances.Get(projectID, zone, instanceName).Do()
	if err != nil {
		return err
	}

	op, err := service.Instances.Delete(projectID, zone, instanceName).Do()
	if err != nil {
		return err
	}

	oco := OpContext{
		Operation:    op,
		ProjectId:    projectID,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())

	return err
}

// ResizeHost change the template used by an host
func (s *Stack) ResizeHost(id string, request resources.SizingRequirements) (*resources.Host, error) {
	return nil, fmt.Errorf("not implemented yet")
}

// ListHosts lists available hosts
func (s *Stack) ListHosts() ([]*resources.Host, error) {
	compuService := s.ComputeService

	var hostList []*resources.Host

	token := ""
	for paginate := true; paginate; {
		resp, err := compuService.Instances.List(s.GcpConfig.ProjectId, s.GcpConfig.Zone).PageToken(token).Do()
		if err != nil {
			return hostList, fmt.Errorf("cannot list hosts: %v", err)
		}
		for _, instance := range resp.Items {
			nhost := resources.NewHost()
			nhost.ID = strconv.FormatUint(instance.Id, 10)
			nhost.Name = instance.Name
			nhost.LastState = stateConvert(instance.Status)

			// FIXME Populate host, what's missing ?
			hostList = append(hostList, nhost)
		}
		token := resp.NextPageToken
		paginate = token != ""
	}

	return hostList, nil
}

// StopHost stops the host identified by id
func (s *Stack) StopHost(id string) error {
	service := s.ComputeService

	op, err := service.Instances.Stop(s.GcpConfig.ProjectId, s.GcpConfig.Zone, id).Do()
	if err != nil {
		return err
	}

	oco := OpContext{
		Operation:    op,
		ProjectId:    s.GcpConfig.ProjectId,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())
	return err
}

// StartHost starts the host identified by id
func (s *Stack) StartHost(id string) error {
	service := s.ComputeService

	op, err := service.Instances.Start(s.GcpConfig.ProjectId, s.GcpConfig.Zone, id).Do()
	if err != nil {
		return err
	}

	oco := OpContext{
		Operation:    op,
		ProjectId:    s.GcpConfig.ProjectId,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())
	return err
}

// RebootHost reboot the host identified by id
func (s *Stack) RebootHost(id string) error {
	service := s.ComputeService

	op, err := service.Instances.Stop(s.GcpConfig.ProjectId, s.GcpConfig.Zone, id).Do()
	if err != nil {
		return err
	}

	oco := OpContext{
		Operation:    op,
		ProjectId:    s.GcpConfig.ProjectId,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())
	if err != nil {
		return err
	}

	op, err = service.Instances.Start(s.GcpConfig.ProjectId, s.GcpConfig.Zone, id).Do()
	if err != nil {
		return err
	}

	oco = OpContext{
		Operation:    op,
		ProjectId:    s.GcpConfig.ProjectId,
		Service:      service,
		DesiredState: "DONE",
	}

	err = waitUntilOperationIsSuccessfulOrTimeout(oco, common.GetMinDelay(), common.GetHostTimeout())
	return err
}

// GetHostState returns the host identified by id
func (s *Stack) GetHostState(hostParam interface{}) (HostState.Enum, error) {
	host, err := s.InspectHost(hostParam)
	if err != nil {
		return HostState.ERROR, err
	}

	return host.LastState, nil
}

//-------------Provider Infos-------------------------------------------------------------------------------------------

// ListAvailabilityZones lists the usable AvailabilityZones
func (s *Stack) ListAvailabilityZones() (map[string]bool, error) {
	regions := make(map[string]bool)

	compuService := s.ComputeService

	resp, err := compuService.Regions.List(s.GcpConfig.ProjectId).Do()
	if err != nil {
		return regions, err
	}
	for _, region := range resp.Items {
		regions[region.Name] = region.Status == "UP"
	}

	return regions, nil
}

// ListRegions ...
func (s *Stack) ListRegions() ([]string, error) {
	// FIXME Implement this

	var regions []string

	return regions, nil
}