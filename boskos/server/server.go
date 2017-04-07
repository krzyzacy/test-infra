/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"k8s.io/test-infra/boskos/common"
)

type Ranch struct {
	Resources   []common.Resource
	lock        sync.RWMutex
	storagePath string
}

// errors
type OwnerNotMatch struct {
	request string
	owner   string
}

func (o OwnerNotMatch) Error() string {
	return fmt.Sprintf("OwnerNotMatch - request by %s, currently owned by %s", o.request, o.owner)
}

type ResourceNotFound struct {
	name string
}

func (r ResourceNotFound) Error() string {
	return fmt.Sprintf("Resource %s not exist", r.name)
}

type StateNotMatch struct {
	expect  string
	current string
}

func (s StateNotMatch) Error() string {
	return fmt.Sprintf("StateNotMatch - expect %v, current %v", s.expect, s.current)
}

func ErrorToStatus(err error) int {
	switch err.(type) {
	default:
		return http.StatusInternalServerError
	case *OwnerNotMatch:
		return http.StatusUnauthorized
	case *ResourceNotFound:
		return http.StatusNotFound
	case *StateNotMatch:
		return http.StatusConflict
	}
}

// NewRanch creates a new Ranch object.
// In: config - path to resource file
//     storage - path to where to save/restore the state data
// Out: A Ranch object, loaded from config/storage, or error
func NewRanch(config string, storage string) (*Ranch, error) {

	newRanch := &Ranch{
		storagePath: storage,
	}

	if storage != "" {
		buf, err := ioutil.ReadFile(storage)
		if err == nil {
			logrus.Infof("Current state: %v.", buf)
			err = json.Unmarshal(buf, newRanch)
			if err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	if err := newRanch.SyncConfig(config); err != nil {
		return nil, err
	}

	newRanch.LogStatus()

	return newRanch, nil
}

// Acquire checks out a type of resource in certain state without an owner
// In: rtype - name of the target resource
//     state - destination state of the resource
//     owner - requester of the resource
// Out: error if owner does not match, or resource does not exist
func (r *Ranch) Acquire(rtype string, state string, owner string) (*common.Resource, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if rtype == res.Type && state == res.State && res.Owner == "" {
			res.LastUpdate = time.Now()
			res.Owner = owner
			return res, nil
		}
	}

	return nil, &ResourceNotFound{rtype}
}

// Release unsets owner for target resource and move it to a new state.
// In: name - name of the target resource
//     dest - destination state of the resource
//     owner - owner of the resource
// Out: error if owner does not match, or resource does not exist
func (r *Ranch) Release(name string, dest string, owner string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if name == res.Name {
			if owner != res.Owner {
				return &OwnerNotMatch{res.Owner, owner}
			}
			res.LastUpdate = time.Now()
			res.Owner = ""
			res.State = dest
			return nil
		}
	}

	return &ResourceNotFound{name}
}

// Update updates the timestamp of a target resource.
// In: name - name of the target resource
//     state - current state of the resource
//     owner - current owner of the resource
// Out: error if state or owner does not match, or resource does not exist
func (r *Ranch) Update(name string, owner string, state string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if name == res.Name {
			if owner != res.Owner {
				return &OwnerNotMatch{res.Owner, owner}
			}

			if state != res.State {
				return &StateNotMatch{res.State, state}
			}
			res.LastUpdate = time.Now()
			return nil
		}
	}

	return &ResourceNotFound{name}
}

// Reset unstucks a type of stale resource to a new state.
// In: rtype - type of the resource
//     state - current state of the resource
//     expire - duration before resource's last update
//     dest - destination state of expired resources
// Out: map of resource name - resource owner
func (r *Ranch) Reset(rtype string, state string, expire time.Duration, dest string) map[string]string {
	r.lock.Lock()
	defer r.lock.Unlock()

	ret := make(map[string]string)

	for idx := range r.Resources {
		res := &r.Resources[idx]
		if rtype == res.Type && state == res.State && res.Owner != "" {
			if time.Now().Sub(res.LastUpdate) > expire {
				res.LastUpdate = time.Now()
				ret[res.Name] = res.Owner
				res.Owner = ""
				res.State = dest
			}
		}
	}

	return ret
}

// LogStatus outputs current status of all resources
func (r *Ranch) LogStatus() {
	r.lock.RLock()
	defer r.lock.RUnlock()

	for _, res := range r.Resources {
		resJSON, _ := json.Marshal(res)
		logrus.Infof("Current Resources : %v", string(resJSON))
	}
}

// SyncConfig updates resource list from a file
func (r *Ranch) SyncConfig(config string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	file, err := ioutil.ReadFile(config)
	if err != nil {
		return err
	}

	var data []common.Resource
	err = json.Unmarshal(file, &data)
	if err != nil {
		return err
	}

	return r.syncConfigHelper(data)
}

func (r *Ranch) syncConfigHelper(data []common.Resource) error {
	// delete non-exist resource
	valid := 0
	for _, res := range r.Resources {
		// If currently busy, yield deletion to later cycles.
		if res.Owner != "" {
			r.Resources[valid] = res
			valid++
			continue
		}

		for _, newRes := range data {
			if res.Name == newRes.Name {
				r.Resources[valid] = res
				valid++
				break
			}
		}
	}
	r.Resources = r.Resources[:valid]

	// add new resource
	for _, p := range data {
		found := false
		for _, exist := range r.Resources {
			if p.Name == exist.Name {
				found = true
				break
			}
		}

		if !found {
			if p.State == "" {
				p.State = "free"
			}
			r.Resources = append(r.Resources, p)
		}
	}
	return nil
}

// SaveState saves current server state in json format
func (r *Ranch) SaveState() {
	if r.storagePath == "" {
		return
	}

	r.lock.RLock()
	defer r.lock.RUnlock()

	// If fail to save data, fatal and restart the server
	buf, err := json.Marshal(r)
	if err != nil {
		logrus.WithError(err).Fatal("Error marshal ranch")
	}
	err = ioutil.WriteFile(r.storagePath+".tmp", buf, 0644)
	if err != nil {
		logrus.WithError(err).Fatal("Error write file")
	}
	err = os.Rename(r.storagePath+".tmp", r.storagePath)
	if err != nil {
		logrus.WithError(err).Fatal("Error rename file")
	}
}
