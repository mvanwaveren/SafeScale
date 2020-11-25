/*
 * Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
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

// func gatewayFromHost(task concurrency.Task, host resources.Host) (resources.Host, fail.Error) {
// 	if task.IsNull() {
// 		return nil, fail.InvalidParameterError("task", "cannot be nil")
// 	}
// 	if host == nil {
// 		return nil, fail.InvalidParameterError("host", "cannot be nil")
// 	}
//
// 	rs, xerr := host.GetDefaultSubnet(task)
// 	if xerr != nil {
// 		return nil, xerr
// 	}
//
// 	gw, xerr := rs.GetGateway(task, true)
// 	if xerr == nil {
// 		_, xerr = gw.WaitSSHReady(task, temporal.GetConnectSSHTimeout())
// 	}
//
// 	if xerr != nil {
// 		if gw, xerr = rs.GetGateway(task, false); xerr == nil {
// 			_, xerr = gw.WaitSSHReady(task, temporal.GetConnectSSHTimeout())
// 		}
// 	}
//
// 	if xerr != nil {
// 		return nil, fail.NotAvailableError("no gateway available")
// 	}
// 	return gw, nil
// }