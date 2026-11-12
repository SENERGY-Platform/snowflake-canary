/*
 * Copyright (c) 2026 InfAI (CC SES)
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

package automation

import "sync"

// Leftovers remembers canary deployments that could not be deleted, so that one of them does not stop
// every later run; they are retried on each cleanup.
type Leftovers struct {
	mux sync.Mutex
	ids map[string]struct{}
}

// Prune forgets remembered ids that are no longer listed (deleted by someone else).
func (l *Leftovers) Prune(listed []string) {
	l.mux.Lock()
	defer l.mux.Unlock()
	present := make(map[string]struct{}, len(listed))
	for _, id := range listed {
		present[id] = struct{}{}
	}
	for id := range l.ids {
		if _, ok := present[id]; !ok {
			delete(l.ids, id)
		}
	}
}

// Cleanup calls del for each id; ids that fail are remembered and passed to onErr, ids that succeed are
// forgotten. Returns the number of remembered ids afterwards.
func (l *Leftovers) Cleanup(ids []string, del func(id string) error, onErr func(id string, err error)) int {
	l.mux.Lock()
	defer l.mux.Unlock()
	if l.ids == nil {
		l.ids = map[string]struct{}{}
	}
	for _, id := range ids {
		if err := del(id); err != nil {
			l.ids[id] = struct{}{}
			onErr(id, err)
		} else {
			delete(l.ids, id)
		}
	}
	return len(l.ids)
}

// Without returns ids that are not remembered, in their original order.
func (l *Leftovers) Without(ids []string) []string {
	l.mux.Lock()
	defer l.mux.Unlock()
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := l.ids[id]; !ok {
			result = append(result, id)
		}
	}
	return result
}
