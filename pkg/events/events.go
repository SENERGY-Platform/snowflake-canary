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

package events

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/SENERGY-Platform/automation-engine/pkg/model"
	devicerepo "github.com/SENERGY-Platform/device-repository/v3/lib/client"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/automation"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/configuration"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/devicemetadata"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/metrics"
)

type Events struct {
	config               configuration.Config
	devicerepo           devicerepo.Interface
	engine               automation.Client
	guaranteeChangeAfter time.Duration
	metrics              *metrics.Metrics
}

type DeviceInfo = devicemetadata.DeviceInfo

func New(config configuration.Config, devicerepo devicerepo.Interface, engine automation.Client, metrics *metrics.Metrics, guaranteeChangeAfter time.Duration) *Events {
	return &Events{
		config:               config,
		devicerepo:           devicerepo,
		engine:               engine,
		guaranteeChangeAfter: guaranteeChangeAfter,
		metrics:              metrics,
	}
}

func (this *Events) getChangeGuaranteeDuration() time.Duration {
	return this.guaranteeChangeAfter
}

func (this *Events) ProcessStartup(token string, info DeviceInfo) error {
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
		if s.LocalId == devicemetadata.SensorServiceLocalId {
			serviceId = s.Id
			break
		}
	}
	if serviceId == "" {
		return errors.New("event no sensor service id found")
	}

	//check prepared deployment
	preparedDepl, err := this.PrepareProcessDeployment(token)
	if err != nil {
		this.metrics.EventProcessPreparedDeploymentErr.Inc()
		this.config.GetLogger().Error("error in PrepareProcessDeployment", "error", err)
	} else {
		if issues := automation.BlockingIssues(preparedDepl.Issues); len(issues) > 0 {
			this.config.GetLogger().Error("prepared deployment has issues", "issues", issues)
		}
		foundDevice, foundService := automation.FindOption(preparedDepl, EventId, model.PreparedEvent, info.Id, serviceId)
		if !foundDevice {
			this.metrics.EventProcessUnexpectedPreparedDeploymentSelectablesErr.Inc()
			this.config.GetLogger().Error("EventProcessUnexpectedPreparedDeploymentSelectablesErr !foundDevice", "device-id", info.Id)
		}
		if !foundService {
			this.metrics.EventProcessUnexpectedPreparedDeploymentSelectablesErr.Inc()
			temp, _ := json.Marshal(preparedDepl)
			this.config.GetLogger().Error("EventProcessUnexpectedPreparedDeploymentSelectablesErr !foundService", "service-id", serviceId, "prepared-deployment", string(temp))
		}
	}

	_, err = this.DeployProcess(token, info.Id, serviceId)
	if err != nil {
		this.metrics.EventProcessDeploymentErr.Inc()
		this.config.GetLogger().Error("error in DeployProcess", "error", err)
		return err
	}

	time.Sleep(this.getChangeGuaranteeDuration())

	return nil
}

func (this *Events) ProcessTeardown(token string) error {
	ids, err := this.ListCanaryProcessDeployments(token)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		return err
	}
	if len(ids) != 1 {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("unexpected process deployment list count", "count", len(ids), "expected", 1)
	}

	instances, err := this.GetProcessInstances(token, ids)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("error in GetProcessInstances", "error", err)
	} else {
		if len(instances) != 1 {
			this.metrics.UncategorizedErr.Inc()
			this.config.GetLogger().Error("unexpected event process instance list count", "instance-count", len(instances), "expected-instance-count", 1, "deployment-count", len(ids))
		} else {
			if instances[0].State != model.StateCompleted {
				this.metrics.UnexpectedEventProcessInstanceStateErr.Inc()
				this.config.GetLogger().Error("unexpected event process instance state", "state", instances[0].State, "expected", model.StateCompleted)
			} else {
				this.metrics.EventProcessInstanceDurationMs.Set(automation.DurationMs(instances[0]))
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

	return nil
}
