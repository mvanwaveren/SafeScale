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

package operations

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/CS-SI/SafeScale/lib/protocol"
	"github.com/CS-SI/SafeScale/lib/server/iaas"
	"github.com/CS-SI/SafeScale/lib/server/iaas/userdata"
	"github.com/CS-SI/SafeScale/lib/server/resources"
	"github.com/CS-SI/SafeScale/lib/server/resources/abstract"
	"github.com/CS-SI/SafeScale/lib/server/resources/enums/networkproperty"
	"github.com/CS-SI/SafeScale/lib/server/resources/enums/networkstate"
	"github.com/CS-SI/SafeScale/lib/server/resources/operations/converters"
	propertiesv1 "github.com/CS-SI/SafeScale/lib/server/resources/properties/v1"
	"github.com/CS-SI/SafeScale/lib/utils"
	"github.com/CS-SI/SafeScale/lib/utils/concurrency"
	"github.com/CS-SI/SafeScale/lib/utils/data"
	"github.com/CS-SI/SafeScale/lib/utils/debug"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/serialize"
	"github.com/CS-SI/SafeScale/lib/utils/strprocess"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

const (
	// networksFolderName is the technical name of the container used to store networks info
	networksFolderName = "networks"
)

// network links Object Storage folder and Network
type network struct {
	*core
}

func nullNetwork() *network {
	return &network{core: nullCore()}
}

// NewNetwork creates an instance of Network
func NewNetwork(svc iaas.Service) (resources.Network, fail.Error) {
	if svc == nil {
		return nullNetwork(), fail.InvalidParameterError("svc", "cannot be nil")
	}

	core, xerr := NewCore(svc, "network", networksFolderName, &abstract.Network{})
	if xerr != nil {
		return nullNetwork(), xerr
	}

	return &network{core: core}, nil
}

// LoadNetwork loads the metadata of a network
func LoadNetwork(task concurrency.Task, svc iaas.Service, ref string) (resources.Network, fail.Error) {
	if task == nil {
		return nullNetwork(), fail.InvalidParameterError("task", "cannot be nil")
	}
	if svc == nil {
		return nullNetwork(), fail.InvalidParameterError("svc", "cannot be nil")
	}
	if ref == "" {
		return nullNetwork(), fail.InvalidParameterError("ref", "cannot be empty string")
	}

	objn, err := NewNetwork(svc)
	if err != nil {
		return nullNetwork(), err
	}
	err = retry.WhileUnsuccessfulDelay1Second(
		func() error {
			return objn.Read(task, ref)
		},
		10*time.Second, // FIXME: parameterize
	)
	if err != nil {
		// If retry timed out, log it and return error ErrNotFound
		if _, ok := err.(*retry.ErrTimeout); ok {
			logrus.Debugf("timeout reading metadata of network '%s'", ref)
			err = fail.NotFoundError("network '%s' not found: %s", ref, fail.RootCause(err).Error())
		}
		return nullNetwork(), err
	}
	return objn, nil
}

// IsNull tells if the instance corresponds to network Null Value
func (objn *network) IsNull() bool {
	return objn == nil || objn.core.IsNull()
}

// Create creates a network
func (objn *network) Create(task concurrency.Task, req abstract.NetworkRequest, gwname string, gwSizing *abstract.HostSizingRequirements) (xerr fail.Error) {
	if objn.IsNull() {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}

	tracer := concurrency.NewTracer(
		task,
		true,
		"('%s', '%s', %s, <sizing>, '%s', %v)", req.Name, req.CIDR, req.IPVersion.String(), req.Image, req.HA,
	).WithStopwatch().Entering()
	defer tracer.OnExitTrace()
	// defer fail.OnExitLogError(&err, tracer.TraceMessage())
	defer fail.OnPanic(&xerr)

	// Check if network already exists and is managed by SafeScale
	svc := objn.SafeGetService()
	if _, xerr = LoadNetwork(task, svc, req.Name); xerr == nil {
		return fail.DuplicateError("network '%s' already exists", req.Name)
	}

	// Verify if the network already exist and in this case is not managed by SafeScale
	if _, xerr = svc.GetNetworkByName(req.Name); xerr != nil {
		switch xerr.(type) {
		case *fail.ErrNotFound:
		case *fail.ErrInvalidRequest, *fail.ErrTimeout:
			return xerr
		default:
			return xerr
		}
	} else {
		return fail.DuplicateError("network '%s' already exists (not managed by SafeScale)", req.Name)
	}

	// Verify the CIDR is not routable
	if req.CIDR != "" {
		routable, xerr := utils.IsCIDRRoutable(req.CIDR)
		if xerr != nil {
			return fail.Wrap(xerr, "failed to determine if CIDR is not routable")
		}
		if routable {
			return fail.InvalidRequestError("cannot create such a network, CIDR must not be routable; please choose an appropriate CIDR (RFC1918)")
		}
	}

	// Create the network
	logrus.Debugf("Creating network '%s' ...", req.Name)
	an, xerr := svc.CreateNetwork(req)
	if xerr != nil {
		switch xerr.(type) {
		case *fail.ErrNotFound, *fail.ErrInvalidRequest, *fail.ErrTimeout:
			return xerr
		default:
			return xerr
		}
	}

	// Starting from here, delete network if exiting with error
	defer func() {
		if xerr != nil && an != nil && !req.KeepOnFailure {
			derr := svc.DeleteNetwork(an.ID)
			if derr != nil {
				switch derr.(type) {
				case *fail.ErrNotFound:
					logrus.Errorf("failed to delete network: resource not found: %+v", derr)
				case *fail.ErrTimeout:
					logrus.Errorf("failed to delete network: timeout: %+v", derr)
				default:
					logrus.Errorf("failed to delete network: %+v", derr)
				}
				_ = xerr.AddConsequence(derr)
			}
		}
	}()

	caps := svc.GetCapabilities()
	failover := req.HA
	if failover {
		if caps.PrivateVirtualIP {
			logrus.Info("Provider support private Virtual IP, honoring the failover setup for gateways.")
		} else {
			logrus.Warning("Provider doesn't support private Virtual IP, cannot set up high availability of network default route.")
			failover = false
		}
	}

	// Creates VIP for gateways if asked for
	if failover {
		if an.VIP, xerr = svc.CreateVIP(an.ID, fmt.Sprintf("for gateways of network %s", an.Name)); xerr != nil {
			switch xerr.(type) {
			case *fail.ErrNotFound, *fail.ErrTimeout:
				return xerr
			default:
				return xerr
			}
		}

		// Starting from here, delete VIP if exists with error
		defer func() {
			if xerr != nil && !req.KeepOnFailure {
				if an != nil {
					derr := svc.DeleteVIP(an.VIP)
					if derr != nil {
						logrus.Errorf("failed to delete VIP: %+v", derr)
						_ = xerr.AddConsequence(derr)
					}
				}
			}
		}()
	}

	// Write network object metadata
	// logrus.Debugf("Saving network metadata '%s' ...", network.Name)
	if xerr = objn.Carry(task, an); xerr != nil {
		return xerr
	}

	// Starting from here, delete network metadata if exits with error
	defer func() {
		if xerr != nil && !req.KeepOnFailure {
			derr := objn.core.Delete(task)
			if derr != nil {
				logrus.Errorf("failed to delete network metadata: %+v", derr)
				_ = xerr.AddConsequence(derr)
			}
		}
	}()

	xerr = objn.Alter(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		an.NetworkState = networkstate.GATEWAY_CREATION
		return nil
	})
	if xerr != nil {
		return xerr
	}

	var template *abstract.HostTemplate
	tpls, xerr := svc.SelectTemplatesBySize(*gwSizing, false)
	if xerr != nil {
		return fail.Wrap(xerr, "failed to find appropriate template")
	}
	if len(tpls) > 0 {
		template = tpls[0]
		msg := fmt.Sprintf("Selected host template: '%s' (%d core%s", template.Name, template.Cores, strprocess.Plural(uint(template.Cores)))
		if template.CPUFreq > 0 {
			msg += fmt.Sprintf(" at %.01f GHz", template.CPUFreq)
		}
		msg += fmt.Sprintf(", %.01f GB RAM, %d GB disk", template.RAMSize, template.DiskSize)
		if template.GPUNumber > 0 {
			msg += fmt.Sprintf(", %d GPU%s", template.GPUNumber, strprocess.Plural(uint(template.GPUNumber)))
			if template.GPUType != "" {
				msg += fmt.Sprintf(" %s", template.GPUType)
			}
		}
		msg += ")"
		logrus.Infof(msg)
	} else {
		return fail.NotFoundError("error creating network: no host template matching requirements for gateway")
	}
	if req.Image == "" {
		// if gwSizing.Image != "" {
		req.Image = gwSizing.Image
		// }
	}
	if req.Image == "" {
		cfg, xerr := svc.GetConfigurationOptions()
		if xerr != nil {
			return xerr
		}
		req.Image = cfg.GetString("DefaultImage")
		gwSizing.Image = req.Image
	}
	img, xerr := svc.SearchImage(req.Image)
	if xerr != nil {
		return fail.Wrap(xerr, "unable to create network gateway")
	}

	networkName := objn.SafeGetName()
	var primaryGatewayName, secondaryGatewayName string
	if failover || gwname == "" {
		primaryGatewayName = "gw-" + networkName
	} else {
		primaryGatewayName = gwname
	}
	if failover {
		secondaryGatewayName = "gw2-" + networkName
	}

	domain := strings.Trim(req.Domain, ".")
	if domain != "" {
		domain = "." + domain
	}

	keypairName := "kp_" + networkName
	keypair, xerr := svc.CreateKeyPair(keypairName)
	if xerr != nil {
		return xerr
	}

	gwRequest := abstract.HostRequest{
		ImageID:       img.ID,
		Networks:      []*abstract.Network{an},
		KeyPair:       keypair,
		TemplateID:    template.ID,
		KeepOnFailure: req.KeepOnFailure,
	}

	var (
		primaryGateway, secondaryGateway   resources.Host
		primaryUserdata, secondaryUserdata *userdata.Content
		primaryTask, secondaryTask         concurrency.Task
		secondaryErr                       fail.Error
		secondaryResult                    concurrency.TaskResult
	)

	// Starts primary gateway creation
	primaryRequest := gwRequest
	primaryRequest.ResourceName = primaryGatewayName
	primaryRequest.HostName = primaryGatewayName + domain
	primaryTask, xerr = task.StartInSubtask(objn.taskCreateGateway, data.Map{
		"request": primaryRequest,
		"sizing":  *gwSizing,
		"primary": true,
	})
	if xerr != nil {
		return xerr
	}

	// Starts secondary gateway creation if asked for
	if failover {
		secondaryRequest := gwRequest
		secondaryRequest.ResourceName = secondaryGatewayName
		secondaryRequest.HostName = secondaryGatewayName
		if req.Domain != "" {
			secondaryRequest.HostName = secondaryGatewayName + domain
		}
		secondaryTask, xerr = task.StartInSubtask(objn.taskCreateGateway, data.Map{
			"request": secondaryRequest,
			"sizing":  *gwSizing,
			"primary": false,
		})
		if xerr != nil {
			return xerr
		}
	}

	primaryResult, primaryErr := primaryTask.Wait()
	if primaryErr == nil {
		result, ok := primaryResult.(data.Map)
		if !ok {
			return fail.InconsistentError("'data.Map' expected, '%s' provided", reflect.TypeOf(primaryResult).String())
		}
		primaryGateway = result["host"].(resources.Host)
		primaryUserdata = result["userdata"].(*userdata.Content)

		// Starting from here, deletes the primary gateway if exiting with error
		defer func() {
			if xerr != nil && !req.KeepOnFailure {
				logrus.Debugf("Cleaning up on failure, deleting gateway '%s'...", primaryGateway.SafeGetName())
				derr := objn.deleteGateway(task, primaryGateway)
				if derr != nil {
					switch derr.(type) {
					case *fail.ErrTimeout:
						logrus.Warnf("We should wait") // FIXME: Wait until gateway no longer exists
					default:
					}
					_ = xerr.AddConsequence(derr)
				} else {
					logrus.Infof("Cleaning up on failure, gateway '%s' deleted", primaryGateway.SafeGetName())
				}
				if failover {
					failErr := objn.unbindHostFromVIP(task, an.VIP, primaryGateway)
					_ = xerr.AddConsequence(failErr)
				}
			}
		}()
	}
	if failover && secondaryTask != nil {
		secondaryResult, secondaryErr = secondaryTask.Wait()
		if secondaryErr == nil {
			result, ok := secondaryResult.(data.Map)
			if !ok {
				return fail.InconsistentError("'data.Map' expected, '%s' provided", reflect.TypeOf(secondaryResult).String())
			}

			secondaryGateway = result["host"].(resources.Host)
			secondaryUserdata = result["userdata"].(*userdata.Content)

			// Starting from here, deletes the secondary gateway if exiting with error
			defer func() {
				if xerr != nil && !req.KeepOnFailure {
					derr := objn.deleteGateway(task, secondaryGateway)
					if derr != nil {
						switch derr.(type) {
						case *fail.ErrTimeout:
							logrus.Warnf("We should wait") // FIXME Wait until gateway no longer exists
						default:
						}
						_ = xerr.AddConsequence(derr)
					}
					failErr := objn.unbindHostFromVIP(task, an.VIP, secondaryGateway)
					if failErr != nil {
						_ = xerr.AddConsequence(failErr)
					}
				}
			}()
		}
	}
	if primaryErr != nil {
		return fail.Wrap(primaryErr, "failed to create gateway '%s'", primaryGatewayName)
	}
	if secondaryErr != nil {
		return fail.Wrap(secondaryErr, "failed to create gateway '%s'", secondaryGatewayName)
	}

	// Update metadata of network object
	xerr = objn.Alter(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}

		// an.GatewayID = primaryGateway.SafeGetID()
		primaryUserdata.PrimaryGatewayPrivateIP = primaryGateway.SafeGetPrivateIP(task)
		primaryUserdata.PrimaryGatewayPublicIP = primaryGateway.SafeGetPublicIP(task)
		primaryUserdata.IsPrimaryGateway = true
		if an.VIP != nil {
			primaryUserdata.DefaultRouteIP = an.VIP.PrivateIP
			primaryUserdata.EndpointIP = an.VIP.PublicIP
		} else {
			primaryUserdata.DefaultRouteIP = primaryUserdata.PrimaryGatewayPrivateIP
			primaryUserdata.EndpointIP = primaryUserdata.PrimaryGatewayPublicIP
		}
		if secondaryGateway != nil {
			// an.SecondaryGatewayID = secondaryGateway.SafeGetID()
			primaryUserdata.SecondaryGatewayPrivateIP = secondaryGateway.SafeGetPrivateIP(task)
			secondaryUserdata.PrimaryGatewayPrivateIP = primaryUserdata.PrimaryGatewayPrivateIP
			secondaryUserdata.SecondaryGatewayPrivateIP = primaryUserdata.SecondaryGatewayPrivateIP
			primaryUserdata.SecondaryGatewayPublicIP = secondaryGateway.SafeGetPublicIP(task)
			secondaryUserdata.PrimaryGatewayPublicIP = primaryUserdata.PrimaryGatewayPublicIP
			secondaryUserdata.SecondaryGatewayPublicIP = primaryUserdata.SecondaryGatewayPublicIP
			secondaryUserdata.IsPrimaryGateway = false
		}

		return nil
	})
	if xerr != nil {
		return xerr
	}

	// As hosts are gateways, the configuration stopped on phase 'netsec', the remaining phases 'hwga', 'sysfix' and 'final' have to be run
	if primaryTask, xerr = concurrency.NewTask(); xerr != nil {
		return xerr
	}
	xerr = objn.Alter(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		an.NetworkState = networkstate.GATEWAY_CONFIGURATION
		return nil
	})
	if xerr != nil {
		return xerr
	}

	primaryTask, xerr = primaryTask.Start(objn.taskFinalizeGatewayConfiguration, data.Map{
		"host":     primaryGateway,
		"userdata": primaryUserdata,
	})
	if xerr != nil {
		return xerr
	}
	if failover && secondaryTask != nil {
		if secondaryTask, xerr = concurrency.NewTask(); xerr != nil {
			return xerr
		}
		secondaryTask, xerr = secondaryTask.Start(objn.taskFinalizeGatewayConfiguration, data.Map{
			"host":     secondaryGateway,
			"userdata": secondaryUserdata,
		})
		if xerr != nil {
			return xerr
		}
	}
	if _, primaryErr = primaryTask.Wait(); primaryErr != nil {
		return primaryErr
	}
	if failover && secondaryTask != nil {
		if _, secondaryErr = secondaryTask.Wait(); secondaryErr != nil {
			return secondaryErr
		}
	}

	// Updates network state in metadata
	// logrus.Debugf("Updating network metadata '%s' ...", network.Name)
	return objn.Alter(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		an.NetworkState = networkstate.READY
		return nil
	})
}

// deleteGateway eases a gateway deletion
// Note: doesn't use gw.Delete() because by rule a Delete on a gateway is not permitted
func (objn *network) deleteGateway(task concurrency.Task, gw resources.Host) (xerr fail.Error) {
	name := gw.SafeGetName()
	fail.OnExitLogError(fmt.Sprintf("failed to delete gateway '%s'", name), &xerr)

	var errors []error
	if xerr = objn.SafeGetService().DeleteHost(gw.SafeGetID()); xerr != nil {
		switch xerr.(type) {
		case *fail.ErrNotFound: // host resource not found, considered as a success.
			break
		case *fail.ErrTimeout:
			errors = append(errors, fail.Wrap(xerr, "failed to delete host '%s', timeout", name))
		default:
			errors = append(errors, fail.Wrap(xerr, "failed to delete host '%s'", name))
		}
	}
	if xerr = gw.(*host).core.Delete(task); xerr != nil {
		switch xerr.(type) {
		case *fail.ErrNotFound: // host metadata not found, considered as a success.
			break
		case *fail.ErrTimeout:
			errors = append(errors, fail.Wrap(xerr, "timeout trying to delete gateway metadata", name))
		default:
			errors = append(errors, fail.Wrap(xerr, "failed to delete gateway '%s' metadata", name))
		}
	}
	if len(errors) > 0 {
		return fail.NewErrorList(errors)
	}
	return nil
}

func (objn *network) unbindHostFromVIP(task concurrency.Task, vip *abstract.VirtualIP, host resources.Host) fail.Error {
	name := host.SafeGetName()
	if xerr := objn.SafeGetService().UnbindHostFromVIP(vip, host.SafeGetID()); xerr != nil {
		switch xerr.(type) {
		case *fail.ErrNotFound, *fail.ErrTimeout:
			logrus.Debugf("Cleaning up on failure, failed to remove '%s' gateway bind from VIP: %v", name, xerr)
		default:
			logrus.Debugf("Cleaning up on failure, failed to remove '%s' gateway bind from VIP: %v", name, xerr)
		}
		return xerr
	}
	logrus.Infof("Cleaning up on failure, host '%s' bind removed from VIP", name)
	return nil
}

// Browse walks through all the metadata objects in network
func (objn *network) Browse(task concurrency.Task, callback func(*abstract.Network) fail.Error) fail.Error {
	if objn.IsNull() {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "can't be nil")
	}
	if callback == nil {
		return fail.InvalidParameterError("callback", "can't be nil")
	}

	return objn.core.BrowseFolder(task, func(buf []byte) fail.Error {
		an := abstract.NewNetwork()
		xerr := an.Deserialize(buf)
		if xerr != nil {
			return xerr
		}
		return callback(an)
	})
}

// AttachHost links host ID to the network
func (objn *network) AttachHost(task concurrency.Task, host resources.Host) (xerr fail.Error) {
	if objn.IsNull() {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}
	if host == nil {
		return fail.InvalidParameterError("host", "cannot be nil")
	}

	tracer := concurrency.NewTracer(nil, true, "("+host.SafeGetName()+")").Entering()
	defer tracer.OnExitTrace()
	// defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)
	defer fail.OnPanic(&xerr)

	hostID := host.SafeGetID()
	hostName := host.SafeGetName()

	return objn.Alter(task, func(_ data.Clonable, props *serialize.JSONProperties) fail.Error {
		return props.Alter(task, networkproperty.HostsV1, func(clonable data.Clonable) fail.Error {
			networkHostsV1, ok := clonable.(*propertiesv1.NetworkHosts)
			if !ok {
				return fail.InconsistentError("'*propertiesv1.NetworkHosts' expected, '%s' provided", reflect.TypeOf(clonable).String())
			}
			networkHostsV1.ByID[hostID] = hostName
			networkHostsV1.ByName[hostName] = hostID
			return nil
		})
	})
}

// DetachHost unlinks host ID from network
func (objn *network) DetachHost(task concurrency.Task, hostID string) (xerr fail.Error) {
	if objn.IsNull() {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}
	if hostID == "" {
		return fail.InvalidParameterError("hostID", "cannot be empty string")
	}

	tracer := concurrency.NewTracer(nil, true, "('"+hostID+"')").Entering()
	defer tracer.OnExitTrace()
	// defer fail.OnExitLogError(&err, tracer.TraceMessage())
	defer fail.OnPanic(&xerr)

	return objn.Alter(task, func(clonable data.Clonable, props *serialize.JSONProperties) fail.Error {
		return props.Alter(task, networkproperty.HostsV1, func(clonable data.Clonable) fail.Error {
			networkHostsV1, ok := clonable.(*propertiesv1.NetworkHosts)
			if !ok {
				return fail.InconsistentError("'*propertiesv1.NetworkHosts' expected, '%s' provided", reflect.TypeOf(clonable).String())
			}
			hostName, found := networkHostsV1.ByID[hostID]
			if found {
				delete(networkHostsV1.ByName, hostName)
				delete(networkHostsV1.ByID, hostID)
			}
			return nil
		})
	})
}

// ListHosts returns the list of Host attached to the network (excluding gateway)
func (objn *network) ListHosts(task concurrency.Task) (_ []resources.Host, xerr fail.Error) {
	if objn.IsNull() {
		return nil, fail.InvalidInstanceError()
	}
	if task == nil {
		return nil, fail.InvalidParameterError("task", "cannot be nil")
	}

	defer concurrency.NewTracer(task, debug.ShouldTrace("resources.network")).Entering().OnExitTrace()
	defer fail.OnExitLogError("error listing hosts", &xerr)
	defer fail.OnPanic(&xerr)

	var list []resources.Host
	xerr = objn.Inspect(task, func(clonable data.Clonable, props *serialize.JSONProperties) fail.Error {
		return props.Inspect(task, networkproperty.HostsV1, func(clonable data.Clonable) fail.Error {
			networkHostsV1, ok := clonable.(*propertiesv1.NetworkHosts)
			if !ok {
				return fail.InconsistentError("'*propertiesv1.NetworkHosts' expected, '%s' provided", reflect.TypeOf(clonable).String())
			}
			svc := objn.SafeGetService()
			for id := range networkHostsV1.ByID {
				host, innerErr := LoadHost(task, svc, id)
				if innerErr != nil {
					return innerErr
				}
				list = append(list, host)
			}
			return nil
		})
	})
	return list, xerr
}

// GetGateway returns the gateway related to network
func (objn *network) GetGateway(task concurrency.Task, primary bool) (_ resources.Host, xerr fail.Error) {
	if objn.IsNull() {
		return nil, fail.InvalidInstanceError()
	}
	if task == nil {
		return nil, fail.InvalidParameterError("task", "cannot be nil")
	}

	defer fail.OnPanic(&xerr)

	primaryStr := "primary"
	if !primary {
		primaryStr = "secondary"
	}
	tracer := concurrency.NewTracer(nil, true, "(%s)", primaryStr).Entering()
	defer tracer.OnExitTrace()
	// defer fail.OnExitLogError(&err, tracer.TraceMessage())
	defer fail.OnPanic(&xerr)

	var gatewayID string
	xerr = objn.Inspect(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		if primary {
			gatewayID = an.GatewayID
		} else {
			gatewayID = an.SecondaryGatewayID
		}
		return nil
	})
	if xerr != nil {
		return nil, xerr
	}
	if gatewayID == "" {
		return nil, fail.NotFoundError("no %s gateway ID found in network properties", primaryStr)
	}
	return LoadHost(task, objn.SafeGetService(), gatewayID)
}

// SafeGetGateway returns a resources.Host corresponding to the gateway requested. May return HostNull if no gateway exists.
func (objn *network) SafeGetGateway(task concurrency.Task, primary bool) resources.Host {
	host, _ := objn.GetGateway(task, primary)
	return host
}

// Delete deletes network referenced by ref
func (objn *network) Delete(task concurrency.Task) (xerr fail.Error) {
	if objn.IsNull() {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}

	tracer := concurrency.NewTracer(nil, true, "").WithStopwatch().Entering()
	defer tracer.OnExitTrace()
	// defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)
	defer fail.OnPanic(&xerr)

	objn.SafeLock(task)
	defer objn.SafeUnlock(task)

	// var gwID string
	xerr = objn.Alter(task, func(clonable data.Clonable, props *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}

		svc := objn.SafeGetService()

		// Check if hosts are still attached to network according to metadata
		var errorMsg string
		innerErr := props.Inspect(task, networkproperty.HostsV1, func(clonable data.Clonable) fail.Error {
			networkHostsV1, ok := clonable.(*propertiesv1.NetworkHosts)
			if !ok {
				return fail.InconsistentError("'*propertiesv1.NetworkHosts' expected, '%s' provided", reflect.TypeOf(clonable).String())
			}
			hostsLen := uint(len(networkHostsV1.ByName))
			if hostsLen > 0 {
				list := make([]string, 0, hostsLen)
				for k := range networkHostsV1.ByName {
					list = append(list, k)
				}
				verb := "are"
				if hostsLen == 1 {
					verb = "is"
				}
				errorMsg = fmt.Sprintf("cannot delete network '%s': %d host%s %s still attached to it: %s",
					an.Name, hostsLen, strprocess.Plural(hostsLen), verb, strings.Join(list, ", "))
				return fail.NotAvailableError(errorMsg)
			}
			return nil
		})
		if innerErr != nil {
			return innerErr
		}

		// Leave a chance to abort
		taskStatus, _ := task.GetStatus()
		if taskStatus == concurrency.ABORTED {
			return fail.AbortedError(nil)
		}

		// 1st delete primary gateway
		if an.GatewayID != "" {
			stop := false
			rh, innerErr := LoadHost(task, svc, an.GatewayID)
			if innerErr != nil {
				if _, ok := innerErr.(*fail.ErrNotFound); !ok {
					return innerErr
				}
				stop = true
			}
			if !stop {
				if rh != nil {
					logrus.Debugf("Deleting gateway '%s'...", rh.SafeGetName())
					innerErr = objn.deleteGateway(task, rh)
					if _, ok := innerErr.(*fail.ErrNotFound); ok { // allow no gateway, but log it
						logrus.Errorf("Failed to delete primary gateway: %s", innerErr.Error())
					} else if innerErr != nil {
						return innerErr
					}
				}
			} else {
				logrus.Infof("Primary Gateway of network '%s' appears to be already deleted", an.Name)
			}
		}

		// 2nd delete secondary gateway
		if an.SecondaryGatewayID != "" {
			stop := false
			rh, innerErr := LoadHost(task, svc, an.SecondaryGatewayID)
			if innerErr != nil {
				if _, ok := innerErr.(*fail.ErrNotFound); !ok {
					return innerErr
				}
				stop = true
			}
			if !stop {
				if rh != nil {
					logrus.Debugf("Deleting gateway '%s'...", rh.SafeGetName())
					innerErr = objn.deleteGateway(task, rh)
					if innerErr != nil { // allow no gateway, but log it
						if _, ok := innerErr.(*fail.ErrNotFound); ok { // nolint
							logrus.Errorf("failed to delete secondary gateway: %s", innerErr.Error())
						} else {
							return innerErr
						}
					}
				}
			} else {
				logrus.Infof("Secondary Gateway of network '%s' appears to be already deleted", an.Name)
			}
		}

		// 3rd delete VIP if needed
		if an.VIP != nil {
			innerErr = svc.DeleteVIP(an.VIP)
			if innerErr != nil {
				// FIXME: THINK Should we exit on failure ?
				logrus.Errorf("failed to delete VIP: %v", innerErr)
			}
		}

		waitMore := false
		// delete network, with tolerance
		innerErr = svc.DeleteNetwork(an.ID)
		if innerErr != nil {
			switch innerErr.(type) {
			case *fail.ErrNotFound:
				// If network doesn't exist anymore on the provider infrastructure, don't fail to cleanup the metadata
				logrus.Warnf("network not found on provider side, cleaning up metadata.")
				return innerErr
			case *fail.ErrTimeout:
				logrus.Error("cannot delete network due to a timeout")
				waitMore = true
			default:
				logrus.Error("cannot delete network, other reason")
			}
		}
		if waitMore {
			errWaitMore := retry.WhileUnsuccessfulDelay1Second(
				func() error {
					recNet, recErr := svc.GetNetwork(an.ID)
					if recNet != nil {
						return fmt.Errorf("still there")
					}
					if _, ok := recErr.(*fail.ErrNotFound); ok {
						return nil
					}
					return fail.Wrap(recErr, "another kind of error")
				},
				temporal.GetContextTimeout(),
			)
			if errWaitMore != nil {
				_ = innerErr.AddConsequence(errWaitMore)
			}
		}
		return innerErr
	})
	if xerr != nil {
		return xerr
	}

	// Delete metadata
	return objn.core.Delete(task)
}

// GetDefaultRouteIP returns the IP of the LAN default route
func (objn *network) GetDefaultRouteIP(task concurrency.Task) (ip string, xerr fail.Error) {
	if objn.IsNull() {
		return "", fail.InvalidInstanceError()
	}
	if task == nil {
		return "", fail.InvalidParameterError("task", "cannot be nil")
	}

	ip = ""
	xerr = objn.Inspect(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		if an.VIP != nil && an.VIP.PrivateIP != "" {
			ip = an.VIP.PrivateIP
		} else {
			objpgw, innerErr := LoadHost(task, objn.SafeGetService(), an.GatewayID)
			if innerErr != nil {
				return innerErr
			}
			ip = objpgw.SafeGetPrivateIP(task)
			return nil
		}
		return nil
	})
	return ip, xerr
}

// SafeGetDefaultRouteIP ...
func (objn *network) SafeGetDefaultRouteIP(task concurrency.Task) string {
	if objn.IsNull() {
		return ""
	}
	ip, _ := objn.GetDefaultRouteIP(task)
	return ip
}

// GetEndpointIP returns the IP of the internet IP to reach the network
func (objn *network) GetEndpointIP(task concurrency.Task) (ip string, xerr fail.Error) {
	if objn.IsNull() {
		return "", fail.InvalidInstanceError()
	}
	if task == nil {
		return "", fail.InvalidParameterError("task", "cannot be nil")
	}

	ip = ""
	xerr = objn.Inspect(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		if an.VIP != nil && an.VIP.PublicIP != "" {
			ip = an.VIP.PublicIP
		} else {
			objpgw, inErr := LoadHost(task, objn.SafeGetService(), an.GatewayID)
			if inErr != nil {
				return inErr
			}
			ip = objpgw.SafeGetPublicIP(task)
			return nil
		}
		return nil
	})
	return ip, xerr
}

// SafeGetEndpointIP ...
func (objn *network) SafeGetEndpointIP(task concurrency.Task) string {
	if objn.IsNull() {
		return ""
	}
	ip, _ := objn.GetEndpointIP(task)
	return ip
}

// HasVirtualIP tells if the network uses a VIP a default route
func (objn *network) HasVirtualIP(task concurrency.Task) bool {
	if objn.IsNull() {
		logrus.Errorf(fail.InvalidInstanceError().Error())
		return false
	}

	var found bool
	xerr := objn.Inspect(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		found = an.VIP != nil
		return nil
	})
	return xerr == nil && found
}

// GetVirtualIP returns an abstract.VirtualIP used by gateway HA
func (objn *network) GetVirtualIP(task concurrency.Task) (vip *abstract.VirtualIP, xerr fail.Error) {
	if objn == nil {
		return nil, fail.InvalidInstanceError()
	}
	if task == nil {
		return nil, fail.InvalidParameterError("task", "cannot be nil")
	}

	xerr = objn.Inspect(task, func(clonable data.Clonable, props *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		vip = an.VIP
		return nil
	})
	if xerr != nil {
		return nil, fail.Wrap(xerr, "cannot get network virtual IP")

	}
	if vip == nil {
		return nil, fail.NotFoundError("failed to find Virtual IP binded to gateways for network '%s'", objn.SafeGetName())
	}
	return vip, nil
}

// GetCIDR returns the CIDR of the network
func (objn *network) GetCIDR(task concurrency.Task) (cidr string, xerr fail.Error) {
	if objn == nil {
		return "", fail.InvalidInstanceError()
	}
	if task == nil {
		return "", fail.InvalidParameterError("task", "cannot be nil")
	}

	cidr = ""
	xerr = objn.Inspect(task, func(clonable data.Clonable, _ *serialize.JSONProperties) fail.Error {
		an, ok := clonable.(*abstract.Network)
		if !ok {
			return fail.InconsistentError("'*abstract.Network' expected, '%s' provided", reflect.TypeOf(clonable).String())
		}
		cidr = an.CIDR
		return nil
	})
	return cidr, xerr
}

// SafeGetCIDR returns the CIDR of the network
// Intended to be used when objn is notoriously not nil (because previously checked)
func (objn *network) SafeGetCIDR(task concurrency.Task) string {
	cidr, _ := objn.GetCIDR(task)
	return cidr
}

// ToProtocol converts resources.Network to protocol.Network
func (objn *network) ToProtocol(task concurrency.Task) (_ *protocol.Network, xerr fail.Error) {
	if objn == nil {
		return nil, fail.InvalidInstanceError()
	}
	if task == nil {
		return nil, fail.InvalidParameterError("task", "cannot be nil")
	}

	tracer := concurrency.NewTracer(task, true, "").Entering()
	defer tracer.OnExitTrace()

	defer func() {
		if xerr != nil {
			xerr = fail.Wrap(xerr, "failed to convert resources.Network to *protocol.Network")
		}
	}()

	var (
		secondaryGatewayID string
		gw                 resources.Host
		vip                *abstract.VirtualIP
	)

	// Get primary gateway ID
	gw, xerr = objn.GetGateway(task, true)
	if xerr != nil {
		return nil, xerr
	}
	primaryGatewayID := gw.SafeGetID()

	// Get secondary gateway id if such a gateway exists
	gw, xerr = objn.GetGateway(task, false)
	if xerr != nil {
		if _, ok := xerr.(*fail.ErrNotFound); !ok {
			return nil, xerr
		}
	} else {
		secondaryGatewayID = gw.SafeGetID()
	}

	pn := &protocol.Network{
		Id:                 objn.SafeGetID(),
		Name:               objn.SafeGetName(),
		Cidr:               objn.SafeGetCIDR(task),
		GatewayId:          primaryGatewayID,
		SecondaryGatewayId: secondaryGatewayID,
		Failover:           objn.HasVirtualIP(task),
		// State:              objn.SafeGetState(),
	}

	vip, xerr = objn.GetVirtualIP(task)
	if xerr != nil {
		if _, ok := xerr.(*fail.ErrNotFound); !ok {
			return nil, xerr
		}
	}
	if vip != nil {
		pn.VirtualIp = converters.VirtualIPFromAbstractToProtocol(*vip)
	}

	return pn, nil
}