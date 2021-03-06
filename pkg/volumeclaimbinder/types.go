/*
Copyright 2014 Google Inc. All rights reserved.

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

package volumeclaimbinder

import (
	"fmt"
	"sort"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/resource"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/cache"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/volume"
)

// persistentVolumeOrderedIndex is a cache.Store that keeps persistent volumes indexed by AccessModes and ordered by storage capacity.
type persistentVolumeOrderedIndex struct {
	cache.Indexer
}

var _ cache.Store = &persistentVolumeOrderedIndex{} // persistentVolumeOrderedIndex is a Store

func NewPersistentVolumeOrderedIndex() *persistentVolumeOrderedIndex {
	return &persistentVolumeOrderedIndex{
		cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{"accessmodes": accessModesIndexFunc}),
	}
}

// accessModesIndexFunc is an indexing function that returns a persistent volume's AccessModes as a string
func accessModesIndexFunc(obj interface{}) (string, error) {
	if pv, ok := obj.(*api.PersistentVolume); ok {
		modes := volume.GetAccessModesAsString(pv.Spec.AccessModes)
		return modes, nil
	}
	return "", fmt.Errorf("object is not a persistent volume: %v", obj)
}

// ListByAccessModes returns all volumes with the given set of AccessModeTypes *in order* of their storage capacity (low to high)
func (pvIndex *persistentVolumeOrderedIndex) ListByAccessModes(modes []api.AccessModeType) ([]*api.PersistentVolume, error) {
	pv := &api.PersistentVolume{
		Spec: api.PersistentVolumeSpec{
			AccessModes: modes,
		},
	}

	objs, err := pvIndex.Index("accessmodes", pv)
	if err != nil {
		return nil, err
	}

	volumes := make([]*api.PersistentVolume, len(objs))
	for i, obj := range objs {
		volumes[i] = obj.(*api.PersistentVolume)
	}

	sort.Sort(byCapacity{volumes})
	return volumes, nil
}

// matchPredicate is a function that indicates that a persistent volume matches another
type matchPredicate func(compareThis, toThis *api.PersistentVolume) bool

// Find returns the nearest PV from the ordered list or nil if a match is not found
func (pvIndex *persistentVolumeOrderedIndex) Find(pv *api.PersistentVolume, matchPredicate matchPredicate) (*api.PersistentVolume, error) {
	volumes, err := pvIndex.ListByAccessModes(pv.Spec.AccessModes)
	if err != nil {
		return nil, err
	}

	i := sort.Search(len(volumes), func(i int) bool { return matchPredicate(pv, volumes[i]) })
	if i < len(volumes) {
		return volumes[i], nil
	}
	return nil, nil
}

// FindByAccessModesAndStorageCapacity is a convenience method that calls Find w/ requisite matchPredicate for storage
func (pvIndex *persistentVolumeOrderedIndex) FindByAccessModesAndStorageCapacity(modes []api.AccessModeType, qty resource.Quantity) (*api.PersistentVolume, error) {
	pv := &api.PersistentVolume{
		Spec: api.PersistentVolumeSpec{
			AccessModes: modes,
			Capacity: api.ResourceList{
				api.ResourceName(api.ResourceStorage): qty,
			},
		},
	}

	return pvIndex.Find(pv, filterBoundVolumes)
}

// FindBestMatchForClaim is a convenience method that finds a volume by the claim's AccessModes and requests for Storage
func (pvIndex *persistentVolumeOrderedIndex) FindBestMatchForClaim(claim *api.PersistentVolumeClaim) (*api.PersistentVolume, error) {
	return pvIndex.FindByAccessModesAndStorageCapacity(claim.Spec.AccessModes, claim.Spec.Resources.Requests[api.ResourceName(api.ResourceStorage)])
}

// byCapacity is used to order volumes by ascending storage size
type byCapacity struct {
	volumes []*api.PersistentVolume
}

func (c byCapacity) Less(i, j int) bool {
	return matchStorageCapacity(c.volumes[i], c.volumes[j])
}

func (c byCapacity) Swap(i, j int) {
	c.volumes[i], c.volumes[j] = c.volumes[j], c.volumes[i]
}

func (c byCapacity) Len() int {
	return len(c.volumes)
}

// matchStorageCapacity is a matchPredicate used to sort and find volumes
func matchStorageCapacity(pvA, pvB *api.PersistentVolume) bool {
	// skip already claimed volumes
	if pvA.Spec.ClaimRef != nil {
		return false
	}

	aQty := pvA.Spec.Capacity[api.ResourceStorage]
	bQty := pvB.Spec.Capacity[api.ResourceStorage]
	aSize := aQty.Value()
	bSize := bQty.Value()
	return aSize <= bSize
}

// filterBoundVolumes is a matchPredicate that filters bound volumes before comparing storage capacity
func filterBoundVolumes(compareThis, toThis *api.PersistentVolume) bool {
	if compareThis.Spec.ClaimRef != nil || toThis.Spec.ClaimRef != nil {
		return false
	}
	return matchStorageCapacity(compareThis, toThis)
}
