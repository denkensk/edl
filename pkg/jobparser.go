/* Copyright (c) 2016 PaddlePaddle Authors All Rights Reserve.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
	 limitations under the License. */

package edl

import (
	"errors"
	"fmt"
	"strconv"

	log "github.com/inconshreveable/log15"
	edlresource "github.com/paddlepaddle/edl/pkg/resource"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	imagePullPolicy = "Always"
)

// JobParser is a interface can parse "TrainingJob" to
// ReplicaSet and job.
type JobParser interface {
	Validate(job *edlresource.TrainingJob) error
	ParseToTrainer(job *edlresource.TrainingJob) *batchv1.Job
	ParseToPserver(job *edlresource.TrainingJob) *v1beta1.ReplicaSet
	ParseToMaster(job *edlresource.TrainingJob) *v1beta1.ReplicaSet
}

// DefaultJobParser implement a basic JobParser.
type DefaultJobParser int

// Validate updates default values for the added job and validates the fields.
func (p *DefaultJobParser) Validate(job *edlresource.TrainingJob) error {
	// Fill in default values
	// FIXME: Need to test. What is the value if specified "omitempty"
	if job.Spec.Port == 0 {
		job.Spec.Port = 7164
	}
	if job.Spec.PortsNum == 0 {
		job.Spec.PortsNum = 1
	}
	if job.Spec.PortsNumForSparse == 0 {
		job.Spec.PortsNumForSparse = 1
	}
	if job.Spec.Image == "" {
		job.Spec.Image = "paddlepaddle/paddlecloud-job"
	}
	if job.Spec.Passes == 0 {
		job.Spec.Passes = 1
	}

	if !job.Spec.FaultTolerant && job.Elastic() {
		return errors.New("max-instances should equal to min-instances when fault_tolerant is disabled")
	}
	// TODO: add validations.
	return nil
}

// ParseToPserver generate a pserver replicaset resource according to "TrainingJob" resource specs.
func (p *DefaultJobParser) ParseToPserver(job *edlresource.TrainingJob) *v1beta1.ReplicaSet {
	replicas := int32(job.Spec.Pserver.MinInstance)
	command := make([]string, 2, 2)
	// FIXME: refine these part.
	if job.Spec.FaultTolerant {
		command = []string{"paddle_k8s", "start_new_pserver"}
	} else {
		command = []string{"paddle_k8s", "start_pserver"}
	}

	return &v1beta1.ReplicaSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "extensions/v1beta1",
			APIVersion: "ReplicaSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      job.ObjectMeta.Name + "-pserver",
			Namespace: job.ObjectMeta.Namespace,
		},
		Spec: v1beta1.ReplicaSetSpec{
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"paddle-job-pserver": job.ObjectMeta.Name},
				},
				Spec: v1.PodSpec{
					Volumes: job.Spec.Volumes,
					Containers: []v1.Container{
						v1.Container{
							Name:      "pserver",
							Image:     job.Spec.Image,
							Ports:     podPorts(job),
							Env:       podEnv(job),
							Command:   command,
							Resources: job.Spec.Pserver.Resources,
						},
					},
					ImagePullSecrets: job.Spec.ImagePullSecrets,
					HostNetwork:      job.Spec.HostNetwork,
				},
			},
		},
	}
}

// ParseToTrainer parse TrainingJob to a kubernetes job resource.
func (p *DefaultJobParser) ParseToTrainer(job *edlresource.TrainingJob) *batchv1.Job {
	replicas := int32(job.Spec.Trainer.MinInstance)
	command := make([]string, 2)
	if job.Spec.FaultTolerant {
		command = []string{"paddle_k8s", "start_new_trainer"}
	} else {
		command = []string{"paddle_k8s", "start_trainer", "v2"}
	}

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      job.ObjectMeta.Name + "-trainer",
			Namespace: job.ObjectMeta.Namespace,
		},
		Spec: batchv1.JobSpec{
			Parallelism: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"paddle-job": job.ObjectMeta.Name},
				},
				Spec: v1.PodSpec{
					Volumes: job.Spec.Volumes,
					Containers: []v1.Container{
						v1.Container{
							Name:            "trainer",
							Image:           job.Spec.Image,
							ImagePullPolicy: imagePullPolicy,
							Command:         command,
							VolumeMounts:    job.Spec.VolumeMounts,
							Ports:           podPorts(job),
							Env:             podEnv(job),
							Resources:       job.Spec.Trainer.Resources,
						},
					},
					ImagePullSecrets: job.Spec.ImagePullSecrets,
					HostNetwork:      job.Spec.HostNetwork,
					RestartPolicy:    "Never",
				},
			},
		},
	}
}

func getEtcdPodSpec(job *edlresource.TrainingJob) *v1.Container {
	command := []string{"etcd", "-name", "etcd0",
		"-advertise-client-urls", "http://$(POD_IP):2379,http://$(POD_IP):4001",
		"-listen-client-urls", "http://0.0.0.0:2379,http://0.0.0.0:4001",
		"-initial-advertise-peer-urls", "http://$(POD_IP):2380",
		"-listen-peer-urls", "http://0.0.0.0:2380",
		"-initial-cluster", "etcd0=http://$(POD_IP):2380",
		"-initial-cluster-state", "new"}

	return &v1.Container{
		Name:            "etcd",
		Image:           "quay.io/coreos/etcd:v3.2.1",
		ImagePullPolicy: imagePullPolicy,
		// TODO(gongwb): etcd ports?
		Env:     podEnv(job),
		Command: command,
	}
}

// ParseToMaster parse TrainingJob to a kubernetes replicaset resource.
func (p *DefaultJobParser) ParseToMaster(job *edlresource.TrainingJob) *v1beta1.ReplicaSet {
	replicas := int32(1)
	// FIXME: refine these part.
	command := []string{"paddle_k8s", "start_master"}

	return &v1beta1.ReplicaSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "extensions/v1beta1",
			APIVersion: "ReplicaSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      job.ObjectMeta.Name + "-master",
			Namespace: job.ObjectMeta.Namespace,
		},
		Spec: v1beta1.ReplicaSetSpec{
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"paddle-job-master": job.ObjectMeta.Name},
				},
				Spec: v1.PodSpec{
					Volumes: job.Spec.Volumes,
					Containers: []v1.Container{
						v1.Container{
							Name:            "master",
							Image:           job.Spec.Image,
							ImagePullPolicy: imagePullPolicy,
							Ports:           masterPorts(job),
							Command:         command,
							VolumeMounts:    job.Spec.VolumeMounts,
							Resources:       job.Spec.Master.Resources,
						},
						*getEtcdPodSpec(job),
					},
					ImagePullSecrets: job.Spec.ImagePullSecrets,
					HostNetwork:      job.Spec.HostNetwork,
				},
			},
		},
	}
}

// -----------------------------------------------------------------------
// general functions that pserver, trainer use the same
// -----------------------------------------------------------------------
func podPorts(job *edlresource.TrainingJob) []v1.ContainerPort {
	log.Debug("get pod ports", "portsnum", job.Spec.PortsNum, "sparse", job.Spec.PortsNumForSparse)
	portsTotal := job.Spec.PortsNum + job.Spec.PortsNumForSparse
	ports := make([]v1.ContainerPort, portsTotal)
	basePort := int32(job.Spec.Port)
	for i := 0; i < portsTotal; i++ {
		log.Debug("adding port ", "base", basePort,
			" total ", portsTotal)
		ports[i] = v1.ContainerPort{
			Name:          fmt.Sprintf("jobport-%d", basePort),
			ContainerPort: basePort,
		}
		basePort++
	}
	return ports
}

func masterPorts(job *edlresource.TrainingJob) []v1.ContainerPort {
	ports := []v1.ContainerPort{
		v1.ContainerPort{
			Name:          "master-port",
			ContainerPort: 8080,
		},
		v1.ContainerPort{
			Name:          "etcd-port",
			ContainerPort: 2379,
		},
	}
	return ports
}

func podEnv(job *edlresource.TrainingJob) []v1.EnvVar {
	needGPU := "0"
	if job.NeedGPU() {
		needGPU = "1"
	}
	trainerCount := 1
	if job.NeedGPU() {
		q := job.Spec.Trainer.Resources.Requests.NvidiaGPU()
		trainerCount = int(q.Value())
	} else {
		q := job.Spec.Trainer.Resources.Requests.Cpu()
		// FIXME: CPU resource value can be less than 1.
		trainerCount = int(q.Value())
	}

	return []v1.EnvVar{
		v1.EnvVar{Name: "PADDLE_JOB_NAME", Value: job.ObjectMeta.Name},
		// NOTICE: TRAINERS, PSERVERS, PADDLE_INIT_NUM_GRADIENT_SERVERS
		//         these env are used for non-faulttolerant training,
		//         use min-instance all the time. When job is elastic,
		//         these envs are not used.
		v1.EnvVar{Name: "TRAINERS", Value: strconv.Itoa(job.Spec.Trainer.MinInstance)},
		v1.EnvVar{Name: "PSERVERS", Value: strconv.Itoa(job.Spec.Pserver.MinInstance)},
		v1.EnvVar{Name: "ENTRY", Value: job.Spec.Trainer.Entrypoint},
		// FIXME: TOPOLOGY deprecated
		v1.EnvVar{Name: "TOPOLOGY", Value: job.Spec.Trainer.Entrypoint},
		v1.EnvVar{Name: "TRAINER_PACKAGE", Value: job.Spec.Trainer.Workspace},
		v1.EnvVar{Name: "PADDLE_INIT_PORT", Value: strconv.Itoa(job.Spec.Port)},
		// PADDLE_INIT_TRAINER_COUNT should be same to gpu number when use gpu
		// and cpu cores when using cpu
		v1.EnvVar{Name: "PADDLE_INIT_TRAINER_COUNT", Value: strconv.Itoa(trainerCount)},
		v1.EnvVar{Name: "PADDLE_INIT_PORTS_NUM", Value: strconv.Itoa(job.Spec.PortsNum)},
		v1.EnvVar{Name: "PADDLE_INIT_PORTS_NUM_FOR_SPARSE", Value: strconv.Itoa(job.Spec.PortsNumForSparse)},
		v1.EnvVar{Name: "PADDLE_INIT_NUM_GRADIENT_SERVERS", Value: strconv.Itoa(job.Spec.Trainer.MinInstance)},
		v1.EnvVar{Name: "PADDLE_INIT_NUM_PASSES", Value: strconv.Itoa(job.Spec.Passes)},
		v1.EnvVar{Name: "PADDLE_INIT_USE_GPU", Value: needGPU},
		v1.EnvVar{Name: "LD_LIBRARY_PATH", Value: "/usr/local/cuda/lib64"},
		v1.EnvVar{Name: "NAMESPACE", ValueFrom: &v1.EnvVarSource{
			FieldRef: &v1.ObjectFieldSelector{
				FieldPath: "metadata.namespace",
			},
		}},
		v1.EnvVar{Name: "POD_IP", ValueFrom: &v1.EnvVarSource{
			FieldRef: &v1.ObjectFieldSelector{
				FieldPath: "status.podIP",
			},
		}},
	}
}

// -----------------------------------------------------------------------
// general functions end
// -----------------------------------------------------------------------
