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

package stats

import (
	"testing"

	gomock "github.com/golang/mock/gomock"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
	cadvisortest "k8s.io/kubernetes/pkg/kubelet/cadvisor/testing"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	containertest "k8s.io/kubernetes/pkg/kubelet/container/testing"
	kubecontainertest "k8s.io/kubernetes/pkg/kubelet/container/testing"
	"k8s.io/kubernetes/pkg/kubelet/kuberuntime"
	"k8s.io/kubernetes/pkg/kubelet/leaky"
	serverstats "k8s.io/kubernetes/pkg/kubelet/server/stats"
	statustest "k8s.io/kubernetes/pkg/kubelet/status/testing"
	"k8s.io/kubernetes/pkg/volume"
)

func TestFilterTerminatedContainerInfoAndAssembleByPodCgroupKey(t *testing.T) {
	const (
		seedPastPod0Infra      = 1000
		seedPastPod0Container0 = 2000
		seedPod0Infra          = 3000
		seedPod0Container0     = 4000
	)
	const (
		namespace = "test"
		pName0    = "pod0"
		cName00   = "c0"
		pName1    = "pod1"
		cName11   = "c1"
		pName2    = "pod2"
		cName22   = "c2"
	)
	infos := map[string]cadvisorapiv2.ContainerInfo{
		// ContainerInfo with past creation time and no CPU/memory usage for
		// simulating uncleaned cgroups of already terminated containers, which
		// should not be shown in the results.
		"/pod0-i-terminated-1":  getTerminatedContainerInfo(seedPastPod0Infra, pName0, namespace, leaky.PodInfraContainerName),
		"/pod0-c0-terminated-1": getTerminatedContainerInfo(seedPastPod0Container0, pName0, namespace, cName00),

		// Same as above but uses the same creation time as the latest
		// containers. They are terminated containers, so they should not be in
		// the results.
		"/pod0-i-terminated-2":  getTerminatedContainerInfo(seedPod0Infra, pName0, namespace, leaky.PodInfraContainerName),
		"/pod0-c0-terminated-2": getTerminatedContainerInfo(seedPod0Container0, pName0, namespace, cName00),

		// The latest containers, which should be in the results.
		"/pod0-i":  getTestContainerInfo(seedPod0Infra, pName0, namespace, leaky.PodInfraContainerName),
		"/pod0-c0": getTestContainerInfo(seedPod0Container0, pName0, namespace, cName00),

		"/pod1-i.slice":  getTestContainerInfo(seedPod0Infra, pName1, namespace, leaky.PodInfraContainerName),
		"/pod1-c1.slice": getTestContainerInfo(seedPod0Container0, pName1, namespace, cName11),

		"/pod2-i-terminated-1": getTerminatedContainerInfo(seedPastPod0Infra, pName2, namespace, leaky.PodInfraContainerName),
		// ContainerInfo with past creation time and no CPU/memory usage for
		// simulating uncleaned cgroups of already terminated containers, which
		// should not be shown in the results.
		"/pod2-c0-terminated-1": getTerminatedContainerInfo(seedPastPod0Container0, pName2, namespace, cName22),
	}
	filteredInfos, allInfos := filterTerminatedContainerInfoAndAssembleByPodCgroupKey(infos)
	assert.Len(t, filteredInfos, 4)
	assert.Len(t, allInfos, 10)
	for _, c := range []string{"/pod0-i", "/pod0-c0"} {
		if _, found := filteredInfos[c]; !found {
			t.Errorf("%q is expected to be in the output\n", c)
		}
	}
	for _, c := range []string{"pod0-i-terminated-1", "pod0-c0-terminated-1", "pod0-i-terminated-2", "pod0-c0-terminated-2", "pod0-i", "pod0-c0", "c1"} {
		if _, found := allInfos[c]; !found {
			t.Errorf("%q is expected to be in the output\n", c)
		}
	}
}

func TestCadvisorListPodStats(t *testing.T) {
	const (
		namespace0 = "test0"
		namespace2 = "test2"
	)
	const (
		seedRoot              = 0
		seedRuntime           = 100
		seedKubelet           = 200
		seedMisc              = 300
		seedPod0Infra         = 1000
		seedPod0Container0    = 2000
		seedPod0Container1    = 2001
		seedPod1Infra         = 3000
		seedPod1Container     = 4000
		seedPod2Infra         = 5000
		seedPod2Container     = 6000
		seedPod3Infra         = 7000
		seedPod3Container0    = 8000
		seedPod3Container1    = 8001
		seedEphemeralVolume1  = 10000
		seedEphemeralVolume2  = 10001
		seedPersistentVolume1 = 20000
		seedPersistentVolume2 = 20001
	)
	const (
		pName0 = "pod0"
		pName1 = "pod1"
		pName2 = "pod0" // ensure pName2 conflicts with pName0, but is in a different namespace
		pName3 = "pod3"
	)
	const (
		cName00 = "c0"
		cName01 = "c1"
		cName10 = "c0" // ensure cName10 conflicts with cName02, but is in a different pod
		cName20 = "c1" // ensure cName20 conflicts with cName01, but is in a different pod + namespace
		cName30 = "c0-init"
		cName31 = "c1"
	)
	const (
		rootfsCapacity    = uint64(10000000)
		rootfsAvailable   = uint64(5000000)
		rootfsInodesFree  = uint64(1000)
		rootfsInodes      = uint64(2000)
		imagefsCapacity   = uint64(20000000)
		imagefsAvailable  = uint64(8000000)
		imagefsInodesFree = uint64(2000)
		imagefsInodes     = uint64(4000)
	)

	prf0 := statsapi.PodReference{Name: pName0, Namespace: namespace0, UID: "UID" + pName0}
	prf1 := statsapi.PodReference{Name: pName1, Namespace: namespace0, UID: "UID" + pName1}
	prf2 := statsapi.PodReference{Name: pName2, Namespace: namespace2, UID: "UID" + pName2}
	prf3 := statsapi.PodReference{Name: pName3, Namespace: namespace0, UID: "UID" + pName3}
	infos := map[string]cadvisorapiv2.ContainerInfo{
		"/":              getTestContainerInfo(seedRoot, "", "", ""),
		"/docker-daemon": getTestContainerInfo(seedRuntime, "", "", ""),
		"/kubelet":       getTestContainerInfo(seedKubelet, "", "", ""),
		"/system":        getTestContainerInfo(seedMisc, "", "", ""),
		// Pod0 - Namespace0
		"/pod0-i":  getTestContainerInfo(seedPod0Infra, pName0, namespace0, leaky.PodInfraContainerName),
		"/pod0-c0": getTestContainerInfo(seedPod0Container0, pName0, namespace0, cName00),
		"/pod0-c1": getTestContainerInfo(seedPod0Container1, pName0, namespace0, cName01),
		// Pod1 - Namespace0
		"/pod1-i":  getTestContainerInfo(seedPod1Infra, pName1, namespace0, leaky.PodInfraContainerName),
		"/pod1-c0": getTestContainerInfo(seedPod1Container, pName1, namespace0, cName10),
		// Pod2 - Namespace2
		"/pod2-i":                        getTestContainerInfo(seedPod2Infra, pName2, namespace2, leaky.PodInfraContainerName),
		"/pod2-c0":                       getTestContainerInfo(seedPod2Container, pName2, namespace2, cName20),
		"/kubepods/burstable/podUIDpod0": getTestContainerInfo(seedPod0Infra, pName0, namespace0, leaky.PodInfraContainerName),
		"/kubepods/podUIDpod1":           getTestContainerInfo(seedPod1Infra, pName1, namespace0, leaky.PodInfraContainerName),
		// Pod3 - Namespace0
		"/pod3-i":       getTestContainerInfo(seedPod3Infra, pName3, namespace0, leaky.PodInfraContainerName),
		"/pod3-c0-init": getTestContainerInfo(seedPod3Container0, pName3, namespace0, cName30),
		"/pod3-c1":      getTestContainerInfo(seedPod3Container1, pName3, namespace0, cName31),
	}

	freeRootfsInodes := rootfsInodesFree
	totalRootfsInodes := rootfsInodes
	rootfs := cadvisorapiv2.FsInfo{
		Capacity:   rootfsCapacity,
		Available:  rootfsAvailable,
		InodesFree: &freeRootfsInodes,
		Inodes:     &totalRootfsInodes,
	}

	freeImagefsInodes := imagefsInodesFree
	totalImagefsInodes := imagefsInodes
	imagefs := cadvisorapiv2.FsInfo{
		Capacity:   imagefsCapacity,
		Available:  imagefsAvailable,
		InodesFree: &freeImagefsInodes,
		Inodes:     &totalImagefsInodes,
	}

	// memory limit overrides for each container (used to test available bytes if a memory limit is known)
	memoryLimitOverrides := map[string]uint64{
		"/":        uint64(1 << 30),
		"/pod2-c0": uint64(1 << 15),
	}
	for name, memoryLimitOverride := range memoryLimitOverrides {
		info, found := infos[name]
		if !found {
			t.Errorf("No container defined with name %v", name)
		}
		info.Spec.Memory.Limit = memoryLimitOverride
		infos[name] = info
	}
	// any container for which cadvisor should return no stats (as might be the case for an exited init container)
	nostatsOverrides := []string{
		"/pod3-c0-init",
	}
	for _, name := range nostatsOverrides {
		info, found := infos[name]
		if !found {
			t.Errorf("No container defined with name %v", name)
		}
		info.Spec.Memory = cadvisorapiv2.MemorySpec{}
		info.Spec.Cpu = cadvisorapiv2.CpuSpec{}
		info.Spec.HasMemory = false
		info.Spec.HasCpu = false
		infos[name] = info
	}

	options := cadvisorapiv2.RequestOptions{
		IdType:    cadvisorapiv2.TypeName,
		Count:     2,
		Recursive: true,
	}

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCadvisor := cadvisortest.NewMockInterface(mockCtrl)
	mockCadvisor.EXPECT().ContainerInfoV2("/", options).Return(infos, nil)
	mockCadvisor.EXPECT().RootFsInfo().Return(rootfs, nil)
	mockCadvisor.EXPECT().ImagesFsInfo().Return(imagefs, nil)

	mockRuntime := containertest.NewMockRuntime(mockCtrl)
	mockRuntime.EXPECT().ImageStats().Return(&kubecontainer.ImageStats{TotalStorageBytes: 123}, nil).AnyTimes()

	ephemeralVolumes := []statsapi.VolumeStats{getPodVolumeStats(seedEphemeralVolume1, "ephemeralVolume1"),
		getPodVolumeStats(seedEphemeralVolume2, "ephemeralVolume2")}
	persistentVolumes := []statsapi.VolumeStats{getPodVolumeStats(seedPersistentVolume1, "persistentVolume1"),
		getPodVolumeStats(seedPersistentVolume2, "persistentVolume2")}
	volumeStats := serverstats.PodVolumeStats{
		EphemeralVolumes:  ephemeralVolumes,
		PersistentVolumes: persistentVolumes,
	}
	p0Time := metav1.Now()
	p1Time := metav1.Now()
	p2Time := metav1.Now()
	p3Time := metav1.Now()
	mockStatus := statustest.NewMockPodStatusProvider(mockCtrl)
	mockStatus.EXPECT().GetPodStatus(types.UID("UID"+pName0)).Return(v1.PodStatus{StartTime: &p0Time}, true)
	mockStatus.EXPECT().GetPodStatus(types.UID("UID"+pName1)).Return(v1.PodStatus{StartTime: &p1Time}, true)
	mockStatus.EXPECT().GetPodStatus(types.UID("UID"+pName2)).Return(v1.PodStatus{StartTime: &p2Time}, true)
	mockStatus.EXPECT().GetPodStatus(types.UID("UID"+pName3)).Return(v1.PodStatus{StartTime: &p3Time}, true)

	resourceAnalyzer := &fakeResourceAnalyzer{podVolumeStats: volumeStats}

	p := NewCadvisorStatsProvider(mockCadvisor, resourceAnalyzer, nil, nil, mockRuntime, mockStatus, NewFakeHostStatsProvider())
	pods, err := p.ListPodStats()
	assert.NoError(t, err)

	assert.Equal(t, 4, len(pods))
	indexPods := make(map[statsapi.PodReference]statsapi.PodStats, len(pods))
	for _, pod := range pods {
		indexPods[pod.PodRef] = pod
	}

	// Validate Pod0 Results
	ps, found := indexPods[prf0]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 2)
	indexCon := make(map[string]statsapi.ContainerStats, len(ps.Containers))
	for _, con := range ps.Containers {
		indexCon[con.Name] = con
	}
	con := indexCon[cName00]
	assert.EqualValues(t, testTime(creationTime, seedPod0Container0).Unix(), con.StartTime.Time.Unix())
	checkCPUStats(t, "Pod0Container0", seedPod0Container0, con.CPU)
	checkMemoryStats(t, "Pod0Conainer0", seedPod0Container0, infos["/pod0-c0"], con.Memory)

	con = indexCon[cName01]
	assert.EqualValues(t, testTime(creationTime, seedPod0Container1).Unix(), con.StartTime.Time.Unix())
	checkCPUStats(t, "Pod0Container1", seedPod0Container1, con.CPU)
	checkMemoryStats(t, "Pod0Container1", seedPod0Container1, infos["/pod0-c1"], con.Memory)

	assert.EqualValues(t, p0Time.Unix(), ps.StartTime.Time.Unix())
	checkNetworkStats(t, "Pod0", seedPod0Infra, ps.Network)
	checkEphemeralStats(t, "Pod0", []int{seedPod0Container0, seedPod0Container1}, []int{seedEphemeralVolume1, seedEphemeralVolume2}, nil, ps.EphemeralStorage)
	if ps.CPU != nil {
		checkCPUStats(t, "Pod0", seedPod0Infra, ps.CPU)
	}
	if ps.Memory != nil {
		checkMemoryStats(t, "Pod0", seedPod0Infra, infos["/pod0-i"], ps.Memory)
	}

	// Validate Pod1 Results
	ps, found = indexPods[prf1]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 1)
	con = ps.Containers[0]
	assert.Equal(t, cName10, con.Name)
	checkCPUStats(t, "Pod1Container0", seedPod1Container, con.CPU)
	checkMemoryStats(t, "Pod1Container0", seedPod1Container, infos["/pod1-c0"], con.Memory)
	checkNetworkStats(t, "Pod1", seedPod1Infra, ps.Network)

	// Validate Pod2 Results
	ps, found = indexPods[prf2]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 1)
	con = ps.Containers[0]
	assert.Equal(t, cName20, con.Name)
	checkCPUStats(t, "Pod2Container0", seedPod2Container, con.CPU)
	checkMemoryStats(t, "Pod2Container0", seedPod2Container, infos["/pod2-c0"], con.Memory)
	checkNetworkStats(t, "Pod2", seedPod2Infra, ps.Network)

	// Validate Pod3 Results

	ps, found = indexPods[prf3]
	assert.True(t, found)
	// /pod3-c0-init has no stats should be filtered
	assert.Len(t, ps.Containers, 1)
	indexCon = make(map[string]statsapi.ContainerStats, len(ps.Containers))
	for _, con := range ps.Containers {
		indexCon[con.Name] = con
	}
	con = indexCon[cName31]
	assert.Equal(t, cName31, con.Name)
	checkCPUStats(t, "Pod3Container1", seedPod3Container1, con.CPU)
	checkMemoryStats(t, "Pod3Container1", seedPod3Container1, infos["/pod3-c1"], con.Memory)
}

func TestCadvisorListPodCPUAndMemoryStats(t *testing.T) {
	const (
		namespace0 = "test0"
		namespace2 = "test2"
	)
	const (
		seedRoot              = 0
		seedRuntime           = 100
		seedKubelet           = 200
		seedMisc              = 300
		seedPod0Infra         = 1000
		seedPod0Container0    = 2000
		seedPod0Container1    = 2001
		seedPod1Infra         = 3000
		seedPod1Container     = 4000
		seedPod2Infra         = 5000
		seedPod2Container     = 6000
		seedEphemeralVolume1  = 10000
		seedEphemeralVolume2  = 10001
		seedPersistentVolume1 = 20000
		seedPersistentVolume2 = 20001
	)
	const (
		pName0 = "pod0"
		pName1 = "pod1"
		pName2 = "pod0" // ensure pName2 conflicts with pName0, but is in a different namespace
	)
	const (
		cName00 = "c0"
		cName01 = "c1"
		cName10 = "c0" // ensure cName10 conflicts with cName02, but is in a different pod
		cName20 = "c1" // ensure cName20 conflicts with cName01, but is in a different pod + namespace
	)

	prf0 := statsapi.PodReference{Name: pName0, Namespace: namespace0, UID: "UID" + pName0}
	prf1 := statsapi.PodReference{Name: pName1, Namespace: namespace0, UID: "UID" + pName1}
	prf2 := statsapi.PodReference{Name: pName2, Namespace: namespace2, UID: "UID" + pName2}
	infos := map[string]cadvisorapiv2.ContainerInfo{
		"/":              getTestContainerInfo(seedRoot, "", "", ""),
		"/docker-daemon": getTestContainerInfo(seedRuntime, "", "", ""),
		"/kubelet":       getTestContainerInfo(seedKubelet, "", "", ""),
		"/system":        getTestContainerInfo(seedMisc, "", "", ""),
		// Pod0 - Namespace0
		"/pod0-i":  getTestContainerInfo(seedPod0Infra, pName0, namespace0, leaky.PodInfraContainerName),
		"/pod0-c0": getTestContainerInfo(seedPod0Container0, pName0, namespace0, cName00),
		"/pod0-c1": getTestContainerInfo(seedPod0Container1, pName0, namespace0, cName01),
		// Pod1 - Namespace0
		"/pod1-i":  getTestContainerInfo(seedPod1Infra, pName1, namespace0, leaky.PodInfraContainerName),
		"/pod1-c0": getTestContainerInfo(seedPod1Container, pName1, namespace0, cName10),
		// Pod2 - Namespace2
		"/pod2-i":                        getTestContainerInfo(seedPod2Infra, pName2, namespace2, leaky.PodInfraContainerName),
		"/pod2-c0":                       getTestContainerInfo(seedPod2Container, pName2, namespace2, cName20),
		"/kubepods/burstable/podUIDpod0": getTestContainerInfo(seedPod0Infra, pName0, namespace0, leaky.PodInfraContainerName),
		"/kubepods/podUIDpod1":           getTestContainerInfo(seedPod1Infra, pName1, namespace0, leaky.PodInfraContainerName),
	}

	// memory limit overrides for each container (used to test available bytes if a memory limit is known)
	memoryLimitOverrides := map[string]uint64{
		"/":        uint64(1 << 30),
		"/pod2-c0": uint64(1 << 15),
	}
	for name, memoryLimitOverride := range memoryLimitOverrides {
		info, found := infos[name]
		if !found {
			t.Errorf("No container defined with name %v", name)
		}
		info.Spec.Memory.Limit = memoryLimitOverride
		infos[name] = info
	}

	options := cadvisorapiv2.RequestOptions{
		IdType:    cadvisorapiv2.TypeName,
		Count:     2,
		Recursive: true,
	}

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCadvisor := cadvisortest.NewMockInterface(mockCtrl)
	mockCadvisor.EXPECT().ContainerInfoV2("/", options).Return(infos, nil)

	ephemeralVolumes := []statsapi.VolumeStats{getPodVolumeStats(seedEphemeralVolume1, "ephemeralVolume1"),
		getPodVolumeStats(seedEphemeralVolume2, "ephemeralVolume2")}
	persistentVolumes := []statsapi.VolumeStats{getPodVolumeStats(seedPersistentVolume1, "persistentVolume1"),
		getPodVolumeStats(seedPersistentVolume2, "persistentVolume2")}
	volumeStats := serverstats.PodVolumeStats{
		EphemeralVolumes:  ephemeralVolumes,
		PersistentVolumes: persistentVolumes,
	}

	resourceAnalyzer := &fakeResourceAnalyzer{podVolumeStats: volumeStats}

	p := NewCadvisorStatsProvider(mockCadvisor, resourceAnalyzer, nil, nil, nil, nil, NewFakeHostStatsProvider())
	pods, err := p.ListPodCPUAndMemoryStats()
	assert.NoError(t, err)

	assert.Equal(t, 3, len(pods))
	indexPods := make(map[statsapi.PodReference]statsapi.PodStats, len(pods))
	for _, pod := range pods {
		indexPods[pod.PodRef] = pod
	}

	// Validate Pod0 Results
	ps, found := indexPods[prf0]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 2)
	indexCon := make(map[string]statsapi.ContainerStats, len(ps.Containers))
	for _, con := range ps.Containers {
		indexCon[con.Name] = con
	}
	con := indexCon[cName00]
	assert.EqualValues(t, testTime(creationTime, seedPod0Container0).Unix(), con.StartTime.Time.Unix())
	checkCPUStats(t, "Pod0Container0", seedPod0Container0, con.CPU)
	checkMemoryStats(t, "Pod0Conainer0", seedPod0Container0, infos["/pod0-c0"], con.Memory)
	assert.Nil(t, con.Rootfs)
	assert.Nil(t, con.Logs)
	assert.Nil(t, con.Accelerators)
	assert.Nil(t, con.UserDefinedMetrics)

	con = indexCon[cName01]
	assert.EqualValues(t, testTime(creationTime, seedPod0Container1).Unix(), con.StartTime.Time.Unix())
	checkCPUStats(t, "Pod0Container1", seedPod0Container1, con.CPU)
	checkMemoryStats(t, "Pod0Container1", seedPod0Container1, infos["/pod0-c1"], con.Memory)
	assert.Nil(t, con.Rootfs)
	assert.Nil(t, con.Logs)
	assert.Nil(t, con.Accelerators)
	assert.Nil(t, con.UserDefinedMetrics)

	assert.EqualValues(t, testTime(creationTime, seedPod0Infra).Unix(), ps.StartTime.Time.Unix())
	assert.Nil(t, ps.EphemeralStorage)
	assert.Nil(t, ps.VolumeStats)
	assert.Nil(t, ps.Network)
	if ps.CPU != nil {
		checkCPUStats(t, "Pod0", seedPod0Infra, ps.CPU)
	}
	if ps.Memory != nil {
		checkMemoryStats(t, "Pod0", seedPod0Infra, infos["/pod0-i"], ps.Memory)
	}

	// Validate Pod1 Results
	ps, found = indexPods[prf1]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 1)
	con = ps.Containers[0]
	assert.Equal(t, cName10, con.Name)
	checkCPUStats(t, "Pod1Container0", seedPod1Container, con.CPU)
	checkMemoryStats(t, "Pod1Container0", seedPod1Container, infos["/pod1-c0"], con.Memory)
	assert.Nil(t, ps.EphemeralStorage)
	assert.Nil(t, ps.VolumeStats)
	assert.Nil(t, ps.Network)

	// Validate Pod2 Results
	ps, found = indexPods[prf2]
	assert.True(t, found)
	assert.Len(t, ps.Containers, 1)
	con = ps.Containers[0]
	assert.Equal(t, cName20, con.Name)
	checkCPUStats(t, "Pod2Container0", seedPod2Container, con.CPU)
	checkMemoryStats(t, "Pod2Container0", seedPod2Container, infos["/pod2-c0"], con.Memory)
	assert.Nil(t, ps.EphemeralStorage)
	assert.Nil(t, ps.VolumeStats)
	assert.Nil(t, ps.Network)
}

func TestCadvisorImagesFsStats(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	var (
		assert       = assert.New(t)
		mockCadvisor = cadvisortest.NewMockInterface(mockCtrl)
		mockRuntime  = containertest.NewMockRuntime(mockCtrl)

		seed        = 1000
		imageFsInfo = getTestFsInfo(seed)
		imageStats  = &kubecontainer.ImageStats{TotalStorageBytes: 100}
	)

	mockCadvisor.EXPECT().ImagesFsInfo().Return(imageFsInfo, nil)
	mockRuntime.EXPECT().ImageStats().Return(imageStats, nil)

	provider := newCadvisorStatsProvider(mockCadvisor, &fakeResourceAnalyzer{}, mockRuntime, nil, NewFakeHostStatsProvider())
	stats, err := provider.ImageFsStats()
	assert.NoError(err)

	assert.Equal(imageFsInfo.Timestamp, stats.Time.Time)
	assert.Equal(imageFsInfo.Available, *stats.AvailableBytes)
	assert.Equal(imageFsInfo.Capacity, *stats.CapacityBytes)
	assert.Equal(imageStats.TotalStorageBytes, *stats.UsedBytes)
	assert.Equal(imageFsInfo.InodesFree, stats.InodesFree)
	assert.Equal(imageFsInfo.Inodes, stats.Inodes)
	assert.Equal(*imageFsInfo.Inodes-*imageFsInfo.InodesFree, *stats.InodesUsed)
}

func TestCadvisorListPodStatsWhenContainerLogFound(t *testing.T) {
	const (
		namespace0 = "test0"
	)
	const (
		seedRoot           = 0
		seedRuntime        = 100
		seedKubelet        = 200
		seedMisc           = 300
		seedPod0Infra      = 1000
		seedPod0Container0 = 0
		seedPod0Container1 = 0
	)
	const (
		pName0 = "pod0"
	)
	const (
		cName00 = "c0"
		cName01 = "c1"
	)
	const (
		rootfsCapacity    = uint64(10000000)
		rootfsAvailable   = uint64(5000000)
		rootfsInodesFree  = uint64(1000)
		rootfsInodes      = uint64(2000)
		imagefsCapacity   = uint64(20000000)
		imagefsAvailable  = uint64(8000000)
		imagefsInodesFree = uint64(2000)
		imagefsInodes     = uint64(4000)
	)

	prf0 := statsapi.PodReference{Name: pName0, Namespace: namespace0, UID: "UID" + pName0}
	infos := map[string]cadvisorapiv2.ContainerInfo{
		"/":              getTestContainerInfo(seedRoot, "", "", ""),
		"/docker-daemon": getTestContainerInfo(seedRuntime, "", "", ""),
		"/kubelet":       getTestContainerInfo(seedKubelet, "", "", ""),
		"/system":        getTestContainerInfo(seedMisc, "", "", ""),
		// Pod0 - Namespace0
		"/pod0-i":  getTestContainerInfo(seedPod0Infra, pName0, namespace0, leaky.PodInfraContainerName),
		"/pod0-c0": getTestContainerInfo(seedPod0Container0, pName0, namespace0, cName00),
		"/pod0-c1": getTestContainerInfo(seedPod0Container1, pName0, namespace0, cName01),
	}

	containerLogStats0 := makeFakeLogStats(0)
	containerLogStats1 := makeFakeLogStats(0)
	fakeStats := map[string]*volume.Metrics{
		kuberuntime.BuildContainerLogsDirectory(prf0.Namespace, prf0.Name, types.UID(prf0.UID), cName00): containerLogStats0,
		kuberuntime.BuildContainerLogsDirectory(prf0.Namespace, prf0.Name, types.UID(prf0.UID), cName01): containerLogStats1,
	}
	fakeStatsSlice := []*volume.Metrics{containerLogStats0, containerLogStats1}
	fakeOS := &kubecontainertest.FakeOS{}

	freeRootfsInodes := rootfsInodesFree
	totalRootfsInodes := rootfsInodes
	rootfs := cadvisorapiv2.FsInfo{
		Capacity:   rootfsCapacity,
		Available:  rootfsAvailable,
		InodesFree: &freeRootfsInodes,
		Inodes:     &totalRootfsInodes,
	}

	freeImagefsInodes := imagefsInodesFree
	totalImagefsInodes := imagefsInodes
	imagefs := cadvisorapiv2.FsInfo{
		Capacity:   imagefsCapacity,
		Available:  imagefsAvailable,
		InodesFree: &freeImagefsInodes,
		Inodes:     &totalImagefsInodes,
	}

	options := cadvisorapiv2.RequestOptions{
		IdType:    cadvisorapiv2.TypeName,
		Count:     2,
		Recursive: true,
	}

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCadvisor := cadvisortest.NewMockInterface(mockCtrl)
	mockCadvisor.EXPECT().ContainerInfoV2("/", options).Return(infos, nil)
	mockCadvisor.EXPECT().RootFsInfo().Return(rootfs, nil)
	mockCadvisor.EXPECT().ImagesFsInfo().Return(imagefs, nil)

	mockRuntime := containertest.NewMockRuntime(mockCtrl)
	mockRuntime.EXPECT().ImageStats().Return(&kubecontainer.ImageStats{TotalStorageBytes: 123}, nil).AnyTimes()

	volumeStats := serverstats.PodVolumeStats{}
	p0Time := metav1.Now()
	mockStatus := statustest.NewMockPodStatusProvider(mockCtrl)
	mockStatus.EXPECT().GetPodStatus(types.UID("UID"+pName0)).Return(v1.PodStatus{StartTime: &p0Time}, true)

	resourceAnalyzer := &fakeResourceAnalyzer{podVolumeStats: volumeStats}

	p := NewCadvisorStatsProvider(mockCadvisor, resourceAnalyzer, nil, nil, mockRuntime, mockStatus, NewFakeHostStatsProviderWithData(fakeStats, fakeOS))
	pods, err := p.ListPodStats()
	assert.NoError(t, err)

	assert.Equal(t, 1, len(pods))
	// Validate Pod0 Results
	checkEphemeralStats(t, "Pod0", []int{seedPod0Container0, seedPod0Container1}, nil, fakeStatsSlice, pods[0].EphemeralStorage)
}
