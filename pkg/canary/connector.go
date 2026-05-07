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

package canary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/SENERGY-Platform/device-repository/lib/model"
	"github.com/SENERGY-Platform/models/go/models"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/configuration"
	"github.com/SENERGY-Platform/snowflake-canary/pkg/devicemetadata"
	paho "github.com/eclipse/paho.mqtt.golang"
)

type PermDevice = devicemetadata.PermDevice

func (this *Canary) checkDeviceConnState(token string, info DeviceInfo, expectedConnState bool) {
	this.metrics.DeviceRepoRequestCount.Inc()
	start := time.Now()
	device, err, _ := this.devicerepo.ReadExtendedDevice(info.Id, token, model.READ, false)
	this.metrics.DeviceRepoRequestLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.config.GetLogger().Error("error in checkDeviceConnState()", "error", err)
		this.metrics.DeviceRepoRequestErr.Inc()
		return
	}
	if (device.ConnectionState == models.ConnectionStateOnline) != expectedConnState {
		this.config.GetLogger().Error("unexpected device connection-state", "actual", device.ConnectionState, "expected", expectedConnState)
		if expectedConnState {
			this.metrics.UnexpectedDeviceOfflineStateErr.Inc()
		} else {
			this.metrics.UnexpectedDeviceOnlineStateErr.Inc()
		}
	}
}

type Conn struct {
	Client paho.Client
}

func (this *Canary) connect(token string, hubId string) (conn *Conn, err error) {
	conn = &Conn{}

	options := paho.NewClientOptions().
		SetClientID(hubId).
		SetAutoReconnect(true).
		SetCleanSession(true).
		AddBroker(this.config.ConnectorMqttBrokerUrl).
		SetConnectionLostHandler(func(c paho.Client, err error) {
			this.config.GetLogger().Error("lost connection", "error", err, "hubId", hubId)
		})

	if this.config.UseCert {
		exp, err := time.ParseDuration(this.config.CertExpTime)
		if err != nil {
			return conn, err
		}
		tlsConf, err := this.getTlsConfig(token, hubId, exp)
		if err != nil {
			return conn, err
		}
		options = options.SetTLSConfig(tlsConf)
	} else {
		options = options.SetUsername(this.config.AuthUsername).SetPassword(this.config.AuthPassword)
	}

	this.metrics.ConnectorLoginCount.Inc()
	conn.Client = paho.NewClient(options)
	start := time.Now()
	conToken := conn.Client.Connect()
	conToken.Wait()
	this.metrics.ConnectorLoginLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if conToken.Error() != nil {
		this.config.GetLogger().Error("error in connect()", "error", conToken.Error())
		this.metrics.ConnectorLoginErr.Inc()
		return conn, conToken.Error()
	}
	return conn, nil
}

func (this *Canary) disconnect(conn *Conn) {
	conn.Client.Disconnect(250)
}

// TODO: add subscription to sensor response
func (this *Canary) subscribe(info DeviceInfo, conn *Conn) {
	this.metrics.ConnectorSubscribeCount.Inc()
	topic := "command/" + info.LocalId + "/+"
	if this.config.TopicsWithOwner {
		topic = "command/" + info.OwnerId + "/" + info.LocalId + "/+"
	}
	start := time.Now()
	token := conn.Client.Subscribe(topic, 2, func(c paho.Client, message paho.Message) {
		err := this.process.NotifyCommand(message.Topic(), message.Payload())
		if err != nil {
			this.config.GetLogger().Error("unexpected command error", "error", err)
			this.metrics.UncategorizedErr.Inc()
			return
		}
		go this.respond(conn, message.Topic(), message.Payload())
	})
	token.Wait()
	this.metrics.ConnectorSubscribeLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if token.Error() != nil {
		this.config.GetLogger().Error("error in subscribe()", "error", token.Error())
		this.metrics.ConnectorSubscribeErr.Inc()
		return
	}
}

type ProtocolSegmentName = string
type CommandRequestMsg = map[ProtocolSegmentName]string
type CommandResponseMsg = map[ProtocolSegmentName]string

type RequestEnvelope struct {
	CorrelationId      string            `json:"correlation_id"`
	Payload            CommandRequestMsg `json:"payload"`
	Time               int64             `json:"timestamp"`
	CompletionStrategy string            `json:"completion_strategy"`
}

type ResponseEnvelope struct {
	CorrelationId string             `json:"correlation_id"`
	Payload       CommandResponseMsg `json:"payload"`
}

func (this *Canary) respond(conn *Conn, cmdtopic string, cmdpayload []byte) {
	request := RequestEnvelope{}
	err := json.Unmarshal(cmdpayload, &request)
	if err != nil {
		this.config.GetLogger().Error("unable to decode request envelope", "error", err, "cmdtopic", cmdtopic, "cmdpayload", string(cmdpayload))
		return
	}

	emptyResp := CommandResponseMsg{}
	for k, _ := range request.Payload {
		emptyResp[k] = ""
	}

	payload, err := json.Marshal(ResponseEnvelope{CorrelationId: request.CorrelationId, Payload: emptyResp})
	if err != nil {
		this.config.GetLogger().Error("unable to encode response envelope", "error", err)
		this.metrics.UncategorizedErr.Inc()
		return
	}

	topic := strings.Replace(cmdtopic, "command/", "response/", 1)

	token := conn.Client.Publish(topic, 2, false, payload)
	token.Wait()
	if token.Error() != nil {
		this.config.GetLogger().Error("unable to publish response", "error", token.Error())
		this.metrics.UncategorizedErr.Inc()
		return
	}
}

func (this *Canary) publish(info DeviceInfo, conn *Conn, value1 int, value2 int) {
	msg, err := getMessage(this.config, value1, value2)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		return
	}

	this.metrics.ConnectorPublishCount.Inc()
	topic := "event/" + info.LocalId + "/sensor"
	if this.config.TopicsWithOwner {
		topic = "event/" + info.OwnerId + "/" + info.LocalId + "/sensor"
	}

	start := time.Now()
	token := conn.Client.Publish(topic, 2, false, msg)
	token.Wait()
	this.metrics.ConnectorPublishLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if token.Error() != nil {
		this.config.GetLogger().Error("unable to publish event", "error", token.Error())
		this.metrics.ConnectorPublishErr.Inc()
		return
	}
}

func getMessage(config configuration.Config, value1 int, value2 int) (payload []byte, err error) {
	xmlMsg := fmt.Sprintf(`<measurements><measurement value="%v" /></measurements>`, value1)
	payload, err = json.Marshal(map[string]string{config.CanaryProtocolSegmentName2: strconv.Itoa(value2), config.CanaryProtocolSegmentName: xmlMsg})
	return
}

type LastValue struct {
	Time  string      `json:"time"`
	Value interface{} `json:"value"`
}

func (this *Canary) checkDeviceValue(token string, info DeviceInfo, value1 int, value2 int) {
	this.metrics.DeviceRepoRequestCount.Inc()
	start := time.Now()
	dt, err, _ := this.devicerepo.ReadDeviceType(info.DeviceTypeId, token)
	this.metrics.DeviceRepoRequestLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.metrics.DeviceRepoRequestErr.Inc()
		this.config.GetLogger().Error("error in checkDeviceValue()", "error", err)
		debug.PrintStack()
		return
	}

	serviceId := ""
	for _, s := range dt.Services {
		if s.LocalId == devicemetadata.SensorServiceLocalId {
			serviceId = s.Id
			break
		}
	}

	buf := &bytes.Buffer{}
	err = json.NewEncoder(buf).Encode([]map[string]interface{}{
		{
			"deviceId":   info.Id,
			"serviceId":  serviceId,
			"columnName": "measurements.measurement.value",
		},
		{
			"deviceId":   info.Id,
			"serviceId":  serviceId,
			"columnName": "area",
		},
	})
	if err != nil {
		return
	}
	this.metrics.DeviceDataRequestCount.Inc()
	req, err := http.NewRequest(http.MethodPost, this.config.LastValueQueryUrl, buf)
	if err != nil {
		this.metrics.UncategorizedErr.Inc()
		this.config.GetLogger().Error("error in checkDeviceValue()", "error", err)
		debug.PrintStack()
		return
	}
	req.Header.Set("Authorization", token)
	start = time.Now()
	lastValues, _, err := devicemetadata.Do[[]LastValue](req)
	this.metrics.DeviceDataRequestLatencyMs.Set(float64(time.Since(start).Milliseconds()))
	if err != nil {
		this.metrics.DeviceDataRequestErr.Inc()
		this.config.GetLogger().Error("error in checkDeviceValue()", "error", err)
		debug.PrintStack()
	}

	expectedValue1 := jsonNormalize(value1)
	expectedValue2 := jsonNormalize(value2)

	if len(lastValues) != 2 {
		this.metrics.UnexpectedDeviceDataErr.Inc()
		this.config.GetLogger().Error("unexpected device data", "lastValues", fmt.Sprintf("%#v", lastValues), "length", len(lastValues), "expected_length", 2)
		return
	}

	if !reflect.DeepEqual(lastValues[0].Value, expectedValue1) {
		this.metrics.UnexpectedDeviceDataErr.Inc()
		this.config.GetLogger().Error("unexpected device data", "lastValues_0", fmt.Sprintf("%#v", lastValues[0].Value), "expected", fmt.Sprintf("%#v", expectedValue1))
	}
	if !reflect.DeepEqual(lastValues[1].Value, expectedValue2) {
		this.metrics.UnexpectedDeviceDataErr.Inc()
		this.config.GetLogger().Error("unexpected device data", "lastValues_1", fmt.Sprintf("%#v", lastValues[1].Value), "expected", fmt.Sprintf("%#v", expectedValue2))

	}
}

func jsonNormalize(in interface{}) (out interface{}) {
	temp, _ := json.Marshal(in)
	json.Unmarshal(temp, &out)
	return
}
