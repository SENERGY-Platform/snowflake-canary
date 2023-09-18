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
	"log"
	"math/rand"
	"sync"
	"time"
)

func (this *Canary) StartTests() {
	go func() {
		log.Println("start canary tests")
		isCurrentlyRunning, done := this.running()
		if isCurrentlyRunning {
			log.Println("test are currently already running")
			return
		}
		defer done()
		defer log.Println("canary tests are finished")
		wg := &sync.WaitGroup{}

		token, refresh, err := this.login()
		if err != nil {
			return
		}
		defer this.logout(token, refresh)

		deviceInfo, err := this.devicemeta.EnsureDevice(token)
		if err != nil {
			return
		}

		this.testDeviceConnection(wg, token, deviceInfo)

		this.testMetadata(wg, token, deviceInfo)

		wg.Wait()

	}()
}

func (this *Canary) testDeviceConnection(wg *sync.WaitGroup, token string, info DeviceInfo) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		eventDeplErr := this.events.ProcessStartup(token, info)

		this.checkDeviceConnState(token, info, false)

		hubId, err := this.ensureHub(token, info)
		if err != nil {
			return
		}

		conn, err := this.connect(hubId)
		if err != nil {
			return
		}

		this.subscribe(info, conn)

		value1 := rand.Int()
		value2 := rand.Int()

		this.publish(info, conn, value1, value2)

		processErr := this.process.ProcessStartup(token, info)

		time.Sleep(this.getChangeGuaranteeDuration())

		this.checkDeviceConnState(token, info, true)

		this.checkDeviceValue(token, info, value1, value2)

		if processErr == nil {
			this.process.ProcessTeardown(token)
		}

		this.disconnect(conn)

		time.Sleep(this.getChangeGuaranteeDuration())

		this.checkDeviceConnState(token, info, false)

		if eventDeplErr == nil {
			this.events.ProcessTeardown(token)
		}

	}()
}
