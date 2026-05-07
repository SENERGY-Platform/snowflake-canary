/*
 * Copyright (c) 2023 InfAI (CC SES)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"time"

	devicerepo "github.com/SENERGY-Platform/device-repository/lib/client"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/configuration"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/devicemetadata"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/metrics"
)

type Process struct {
	config               configuration.Config
	devicerepo           devicerepo.Interface
	guaranteeChangeAfter time.Duration
	receivedCommands     atomic.Int64
	metrics              *metrics.Metrics
}

type DeviceInfo = devicemetadata.DeviceInfo

func New(config configuration.Config, devicerepo devicerepo.Interface, metrics *metrics.Metrics, guaranteeChangeAfter time.Duration) *Process {
	return &Process{
		config:               config,
		devicerepo:           devicerepo,
		guaranteeChangeAfter: guaranteeChangeAfter,
		metrics:              metrics,
	}
}

func (this *Process) getChangeGuaranteeDuration() time.Duration {
	return this.guaranteeChangeAfter
}

// TODO: update process to new commands
// TODO: add seneor command and check response
func (this *Process) ProcessStartup(token string, info DeviceInfo) error {
	this.receivedCommands.Store(0)
	ids, err := this.ListCanaryProcessDeployments(token)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("error in ListCanaryProcessDeployments", "error", err)
		return err
	}
	//cleanup
	for _, id := range ids {
		err = this.DeleteProcess(token, id)
		if err != nil {
			this.metrics.UncategorizedErr.Inc()
			this.config.GetLogger().Error("error in DeleteProcess", "error", err)
			return err
		}
	}

	dt, err, _ := this.devicerepo.ReadDeviceType(info.DeviceTypeId, token)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("error in ReadDeviceType", "error", err)
		return err
	}
	serviceId := ""
	for _, s := range dt.Services {
		if s.LocalId == devicemetadata.CmdServiceLocalId {
			serviceId = s.Id
			break
		}
	}
	if serviceId == "" {
		return errors.New("ProcessStartup(): no cmd service id found")
	}

	//check prepared deployment
	preparedDepl, err := this.PrepareProcessDeployment(token)
	if err != nil {
		this.metrics.ProcessPreparedDeploymentErr.Inc()
		this.config.GetLogger().Error("error in PrepareProcessDeployment", "error", err)
	} else {
		foundService := false
		foundDevice := false
		for _, e := range preparedDepl.Elements {
			if e.BpmnId == "Task_0yuqb45" && e.Task != nil {
				for _, o := range e.Task.Selection.SelectionOptions {
					if o.Device != nil && o.Device.Id == info.Id {
						foundDevice = true
					}
					for _, s := range o.Services {
						if s.Id == serviceId {
							foundService = true
						}
					}
				}
			}
		}
		if !foundDevice {
			this.metrics.ProcessUnexpectedPreparedDeploymentSelectablesErr.Inc()
			this.config.GetLogger().Error("ProcessUnexpectedPreparedDeploymentSelectablesErr !foundDevice", "device-id", info.Id)
		}
		if !foundService {
			this.metrics.ProcessUnexpectedPreparedDeploymentSelectablesErr.Inc()
			temp, _ := json.Marshal(preparedDepl)
			this.config.GetLogger().Error("ProcessUnexpectedPreparedDeploymentSelectablesErr !foundService", "service-id", serviceId, "prepared-deployment", string(temp))
		}
	}

	deplId, err := this.DeployProcess(token, info.Id, serviceId)
	if err != nil {
		this.metrics.ProcessDeploymentErr.Inc()
		this.config.GetLogger().Error("error in DeployProcess", "error", err)
		return err
	}

	time.Sleep(this.getChangeGuaranteeDuration())

	err = this.StartProcess(token, deplId)
	if err != nil {
		this.metrics.ProcessStartErr.Inc()
		this.config.GetLogger().Error("error in StartProcess", "error", err)
		return err
	}

	return nil
}

func (this *Process) ProcessTeardown(token string) error {
	ids, err := this.ListCanaryProcessDeployments(token)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		return err
	}
	if len(ids) != 1 {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("unexpected process deployment list count", "count", len(ids), "expected", 1)
	}

	unfilteredInstances, err := this.GetProcessInstances(token)
	instances := []ProcessInstance{}
	for _, e := range unfilteredInstances {
		if e.ProcessDefinitionName == ExpectedCanaryDeploymentName {
			instances = append(instances, e)
		}
	}

	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("error in GetProcessInstances", "error", err)
	} else {
		if len(instances) != 1 {
			this.metrics.UncategorizedErr.Inc()
			this.config.GetLogger().Error("unexpected process instance list count", "instance-count", len(instances), "expected-instance-count", 1, "deployment-count", len(ids), "unfiltered-instance-count", len(unfilteredInstances))
		} else {
			if instances[0].State != "COMPLETED" {
				this.metrics.UnexpectedProcessInstanceStateErr.Inc()
				this.config.GetLogger().Error("unexpected process instance state", "state", instances[0].State, "expected", "COMPLETED")
			} else {
				this.metrics.ProcessInstanceDurationMs.Set(float64(instances[0].DurationInMillis))
			}
		}
	}

	//cleanup
	for _, id := range ids {
		err = this.DeleteProcess(token, id)
		if err != nil {
			this.metrics.UncategorizedErr.Inc()
			this.config.GetLogger().Error("error in DeleteProcess", "error", err)
			return err
		}
	}

	if this.receivedCommands.Load() == 0 {
		this.metrics.ProcessUnexpectedCommandCountError.Inc()
		this.config.GetLogger().Error("ProcessUnexpectedCommandCountError", "receivedCommands", this.receivedCommands.Load())
	}
	return nil
}

func (this *Process) NotifyCommand(topic string, payload []byte) error {
	this.receivedCommands.Add(1)
	message := RequestEnvelope{}
	err := json.Unmarshal(payload, &message)
	if err != nil {
		this.config.GetLogger().Error("unable to json unmarshal notify command", "error", err, "topic", topic, "payload", string(payload))
		return err
	}
	expectedMessagePayload := map[string]string{
		"data":     `<commands><valueCommand value="42"/></commands>`,
		"metadata": "on",
	}
	if !reflect.DeepEqual(message.Payload, expectedMessagePayload) {
		return errors.New("unexpected command message:" + fmt.Sprintf("%#v", message.Payload))
	}
	return nil
}

type RequestEnvelope struct {
	CorrelationId      string            `json:"correlation_id"`
	Payload            map[string]string `json:"payload"`
	Time               int64             `json:"timestamp"`
	CompletionStrategy string            `json:"completion_strategy"`
}
