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

package devicemetadata

import (
	"bytes"
	"encoding/json"
	"errors"
	devicerepo "github.com/SENERGY-Platform/device-repository/lib/client"
	"github.com/SENERGY-Platform/models/go/models"
	"github.com/SENERGY-Platform/permission-search/lib/client"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/configuration"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/metrics"
	"github.com/google/uuid"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

type DeviceMetaData struct {
	permissions          client.Client
	devicerepo           devicerepo.Interface
	metrics              *metrics.Metrics
	config               configuration.Config
	guaranteeChangeAfter time.Duration
}

func NewDeviceMetaData(permissions client.Client, devicerepo devicerepo.Interface, metrics *metrics.Metrics, config configuration.Config, guaranteeChangeAfter time.Duration) *DeviceMetaData {
	return &DeviceMetaData{permissions: permissions, devicerepo: devicerepo, metrics: metrics, config: config, guaranteeChangeAfter: guaranteeChangeAfter}
}

func (this *DeviceMetaData) getChangeGuaranteeDuration() time.Duration {
	return this.guaranteeChangeAfter
}

func (this *DeviceMetaData) EnsureDevice(token string) (device DeviceInfo, err error) {
	canaryDevices, err := this.ListCanaryDevices(token)
	if err != nil {
		return device, err
	}
	if len(canaryDevices) > 0 {
		return canaryDevices[0], nil
	} else {
		return this.CreateCanaryDevice(token)
	}
}

func (this *DeviceMetaData) ListCanaryDevices(token string) (devices []DeviceInfo, err error) {
	start := time.Now()
	devices, _, err = client.Query[[]DeviceInfo](this.permissions, token, client.QueryMessage{
		Resource: "devices",
		Find: &client.QueryFind{
			QueryListCommons: client.QueryListCommons{
				Limit:  1,
				Offset: 0,
				Rights: "r",
				SortBy: "name",
			},
			Filter: &client.Selection{
				Condition: client.ConditionConfig{
					Feature:   "features.attributes.key",
					Operation: client.QueryEqualOperation,
					Value:     AttributeUsedForCanaryDevice,
				},
			},
		},
	})
	this.metrics.PermissionsRequestCount.Inc()
	this.metrics.PermissionsRequestLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		log.Println("ERROR: ListCanaryDevices()", err)
		this.metrics.PermissionsRequestErr.Inc()
	}
	return devices, err
}

func (this *DeviceMetaData) CreateCanaryDevice(token string) (device DeviceInfo, err error) {
	dt, err := this.EnsureDeviceType(token)
	if err != nil {
		return device, err
	}
	device = DeviceInfo{
		LocalId: "snowflake-canary_" + uuid.NewString(),
		Name:    "snowflake-canary-" + time.Now().String(),
		Attributes: []models.Attribute{{
			Key:    AttributeUsedForCanaryDevice,
			Value:  "true",
			Origin: "canary",
		}},
		DeviceTypeId: dt.Id,
	}

	buf := &bytes.Buffer{}
	err = json.NewEncoder(buf).Encode(device)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
		return device, err
	}
	this.metrics.DeviceMetaUpdateCount.Inc()
	req, err := http.NewRequest(http.MethodPost, this.config.DeviceManagerUrl+"/devices", buf)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
		return device, err
	}
	req.Header.Set("Authorization", token)
	start := time.Now()
	device, _, err = Do[DeviceInfo](req)
	this.metrics.DeviceMetaUpdateLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.metrics.DeviceMetaUpdateErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
	}
	time.Sleep(this.getChangeGuaranteeDuration()) //ensure device is finished creating
	return device, err
}

func (this *DeviceMetaData) EnsureDeviceType(token string) (result DeviceTypeInfo, err error) {
	canaryDeviceTypes, err := this.ListCanaryDeviceTypes(token)
	if err != nil {
		return result, err
	}
	if len(canaryDeviceTypes) > 0 {
		return canaryDeviceTypes[0], nil
	} else {
		return this.CreateCanaryDeviceType(token)
	}
}

func (this *DeviceMetaData) ListCanaryDeviceTypes(token string) (dts []DeviceTypeInfo, err error) {
	start := time.Now()
	dts, _, err = client.Query[[]DeviceTypeInfo](this.permissions, token, client.QueryMessage{
		Resource: "device-types",
		Find: &client.QueryFind{
			QueryListCommons: client.QueryListCommons{
				Limit:  1,
				Offset: 0,
				Rights: "r",
				SortBy: "name",
			},
			Filter: &client.Selection{
				Condition: client.ConditionConfig{
					Feature:   "features.attributes.key",
					Operation: client.QueryEqualOperation,
					Value:     AttributeUsedForCanaryDeviceType,
				},
			},
		},
	})
	this.metrics.PermissionsRequestCount.Inc()
	this.metrics.PermissionsRequestLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.metrics.PermissionsRequestErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
	}
	return dts, err
}

func (this *DeviceMetaData) CreateCanaryDeviceType(token string) (deviceType DeviceTypeInfo, err error) {
	dt := models.DeviceType{
		Name:          "snowflake-canary-device-type",
		Description:   "used for canary service github.com/SENERGY-Platform/snowflake-canary",
		DeviceClassId: this.config.CanaryDeviceClassId,
		Attributes: []models.Attribute{{
			Key:    AttributeUsedForCanaryDeviceType,
			Value:  "true",
			Origin: "canary",
		}},
		Services: []models.Service{
			{
				LocalId:     CmdServiceLocalId,
				Name:        "cmd",
				Description: "canary cmd service, needed to test online state by subscription",
				Interaction: models.REQUEST,
				ProtocolId:  this.config.CanaryProtocolId,
				Inputs: []models.Content{
					{
						ContentVariable: models.ContentVariable{
							Name: "commands",
							Type: models.Structure,
							SubContentVariables: []models.ContentVariable{
								{
									Name: "valueCommand",
									Type: models.Structure,
									SubContentVariables: []models.ContentVariable{
										{
											Name:                 "value",
											Type:                 models.Type(this.config.CanaryCmdValueType),
											CharacteristicId:     this.config.CanaryCmdCharacteristicId,
											FunctionId:           this.config.CanaryCmdFunctionId,
											SerializationOptions: []string{models.SerializationOptionXmlAttribute},
										},
									},
								},
							},
						},
						Serialization:     models.XML,
						ProtocolSegmentId: this.config.CanaryProtocolSegmentId,
					},
					{
						ContentVariable: models.ContentVariable{
							Name:             "flag",
							Type:             models.Type(this.config.CanaryCmdValueType2),
							CharacteristicId: this.config.CanaryCmdCharacteristicId2,
							FunctionId:       this.config.CanaryCmdFunctionId2,
							Value:            this.config.CanaryCmdCharacteristicId2DefaultValue,
						},
						Serialization:     models.PlainText,
						ProtocolSegmentId: this.config.CanaryProtocolSegmentId2,
					},
				},
			},
			{
				LocalId:     SensorServiceLocalId,
				Name:        "sensor",
				Description: "canary sensor service, needed to test device data handling",
				Interaction: models.EVENT_AND_REQUEST,
				ProtocolId:  this.config.CanaryProtocolId,
				Outputs: []models.Content{
					{
						ContentVariable: models.ContentVariable{
							Name: "measurements",
							Type: models.Structure,
							SubContentVariables: []models.ContentVariable{
								{
									Name: "measurement",
									Type: models.Structure,
									SubContentVariables: []models.ContentVariable{
										{
											Name:                 "value",
											Type:                 models.Type(this.config.CanarySensorValueType),
											CharacteristicId:     this.config.CanarySensorCharacteristicId,
											FunctionId:           this.config.CanarySensorFunctionId,
											AspectId:             this.config.CanarySensorAspectId,
											SerializationOptions: []string{models.SerializationOptionXmlAttribute},
										},
									},
								},
							},
						},
						Serialization:     models.XML,
						ProtocolSegmentId: this.config.CanaryProtocolSegmentId,
					},
					{
						ContentVariable: models.ContentVariable{
							Name:             "area",
							Type:             models.Type(this.config.CanarySensorValueType2),
							CharacteristicId: this.config.CanarySensorCharacteristicId2,
							FunctionId:       this.config.CanarySensorFunctionId2,
							AspectId:         this.config.CanarySensorAspectId2,
						},
						Serialization:     models.JSON,
						ProtocolSegmentId: this.config.CanaryProtocolSegmentId2,
					},
				},
			},
		},
	}

	buf := &bytes.Buffer{}
	err = json.NewEncoder(buf).Encode(dt)
	if err != nil {
		return deviceType, err
	}
	this.metrics.DeviceMetaUpdateCount.Inc()
	req, err := http.NewRequest(http.MethodPost, this.config.DeviceManagerUrl+"/device-types", buf)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
		return deviceType, err
	}
	req.Header.Set("Authorization", token)
	start := time.Now()
	deviceType, _, err = Do[DeviceTypeInfo](req)
	this.metrics.DeviceMetaUpdateLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.metrics.DeviceMetaUpdateErr.Inc()
		log.Println("ERROR:", err)
		debug.PrintStack()
	}
	time.Sleep(this.getChangeGuaranteeDuration()) //ensure device-type is finished creating
	return deviceType, err
}

func Do[T any](req *http.Request) (result T, code int, err error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, http.StatusInternalServerError, err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 299 {
		temp, _ := io.ReadAll(resp.Body) //read error response end ensure that resp.Body is read to EOF
		return result, resp.StatusCode, errors.New(string(temp))
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		_, _ = io.ReadAll(resp.Body) //ensure resp.Body is read to EOF
		return result, http.StatusInternalServerError, err
	}
	return result, resp.StatusCode, nil
}
