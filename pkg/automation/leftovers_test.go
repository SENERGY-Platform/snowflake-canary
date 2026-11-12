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

import (
	"errors"
	"reflect"
	"testing"
)

func failFor(bad ...string) func(string) error {
	return func(id string) error {
		for _, b := range bad {
			if id == b {
				return errors.New("forbidden")
			}
		}
		return nil
	}
}

func TestLeftoversRemembersFailedDelete(t *testing.T) {
	var l Leftovers
	var reported []string
	n := l.Cleanup([]string{"a", "b", "c"}, failFor("b"), func(id string, err error) { reported = append(reported, id) })
	if n != 1 {
		t.Errorf("expected 1 leftover, got %d", n)
	}
	if !reflect.DeepEqual(reported, []string{"b"}) {
		t.Errorf("unexpected onErr ids: %v", reported)
	}
	if got := l.Without([]string{"a", "b", "c"}); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("unexpected Without result: %v", got)
	}
}

func TestLeftoversForgetsAfterSuccessfulDelete(t *testing.T) {
	var l Leftovers
	l.Cleanup([]string{"b"}, failFor("b"), func(string, error) {})
	n := l.Cleanup([]string{"b"}, failFor(), func(string, error) { t.Error("unexpected onErr") })
	if n != 0 {
		t.Errorf("expected 0 leftovers, got %d", n)
	}
	if got := l.Without([]string{"b"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("unexpected Without result: %v", got)
	}
}

func TestLeftoversPruneDropsUnlisted(t *testing.T) {
	var l Leftovers
	l.Cleanup([]string{"a", "b"}, failFor("a", "b"), func(string, error) {})
	l.Prune([]string{"b", "c"})
	if got := l.Without([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("unexpected Without result: %v", got)
	}
}

func TestLeftoversZeroValue(t *testing.T) {
	var l Leftovers
	l.Prune([]string{"a"})
	if got := l.Without([]string{"a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("unexpected Without result: %v", got)
	}
	if n := l.Cleanup(nil, failFor(), func(string, error) {}); n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}
