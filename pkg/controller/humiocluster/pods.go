package humiocluster

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/humio/humio-operator/pkg/helpers"

	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/humio/humio-operator/pkg/apis/core/v1alpha1"
	"github.com/humio/humio-operator/pkg/kubernetes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	humioAppPath     = "/app/humio"
	humioDataPath    = "/data/humio-data"
	humioDataTmpPath = "/app/humio/humio-data/tmp"
	sharedPath       = "/shared"
	tmpPath          = "/tmp"
)

type podLifecycleState struct {
	pod           corev1.Pod
	restartPolicy string
	delete        bool
}

func constructPod(hc *corev1alpha1.HumioCluster, dataVolumeSource corev1.VolumeSource) (*corev1.Pod, error) {
	var pod corev1.Pod
	mode := int32(420)
	productVersion := "unknown"
	imageSplit := strings.SplitN(hc.Spec.Image, ":", 2)
	if len(imageSplit) == 2 {
		productVersion = imageSplit[1]
	}
	boolFalse := bool(false)
	boolTrue := bool(true)
	userID := int64(65534)

	pod = corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-core-%s", hc.Name, kubernetes.RandomString()),
			Namespace: hc.Namespace,
			Labels:    kubernetes.LabelsForHumio(hc.Name),
			Annotations: map[string]string{
				"productID":      "none",
				"productName":    "humio",
				"productVersion": productVersion,
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: humioServiceAccountNameOrDefault(hc),
			ImagePullSecrets:   imagePullSecretsOrDefault(hc),
			Subdomain:          hc.Name,
			InitContainers: []corev1.Container{
				{
					Name:  "zookeeper-prefix",
					Image: "humio/humio-operator-helper:0.0.2",
					Env: []corev1.EnvVar{
						{
							Name:  "MODE",
							Value: "init",
						},
						{
							Name:  "TARGET_FILE",
							Value: fmt.Sprintf("%s/zookeeper-prefix", sharedPath),
						},
						{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "shared",
							MountPath: sharedPath,
						},
						{
							Name:      "init-service-account-secret",
							MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
							ReadOnly:  true,
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(50*1024*1024, resource.BinarySI),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(50*1024*1024, resource.BinarySI),
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged:               &boolFalse,
						AllowPrivilegeEscalation: &boolFalse,
						ReadOnlyRootFilesystem:   &boolTrue,
						RunAsUser:                &userID,
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{
								"ALL",
							},
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "auth",
					Image: "humio/humio-operator-helper:0.0.2",
					Env: []corev1.EnvVar{
						{
							Name: "NAMESPACE",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
								},
							},
						},
						{
							Name:  "MODE",
							Value: "auth",
						},
						{
							Name:  "ADMIN_SECRET_NAME",
							Value: kubernetes.ServiceTokenSecretName,
						},
						{
							Name:  "CLUSTER_NAME",
							Value: hc.Name,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "humio-data",
							MountPath: humioDataPath,
							ReadOnly:  true,
						},
						{
							Name:      "auth-service-account-secret",
							MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
							ReadOnly:  true,
						},
					},
					ReadinessProbe: &corev1.Probe{
						Handler: corev1.Handler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/",
								Port: intstr.IntOrString{IntVal: 8180},
							},
						},
					},
					LivenessProbe: &corev1.Probe{
						Handler: corev1.Handler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/",
								Port: intstr.IntOrString{IntVal: 8180},
							},
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(50*1024*1024, resource.BinarySI),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(50*1024*1024, resource.BinarySI),
						},
					},
					SecurityContext: containerSecurityContextOrDefault(hc),
				},
				{
					Name:    "humio",
					Image:   hc.Spec.Image,
					Command: []string{"/bin/sh"},
					Args: []string{"-c",
						fmt.Sprintf("export ZOOKEEPER_PREFIX_FOR_NODE_UUID=/humio_$(cat %s/zookeeper-prefix)_ && exec bash %s/run.sh",
							sharedPath, humioAppPath)},
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: humioPort,
							Protocol:      "TCP",
						},
						{
							Name:          "es",
							ContainerPort: elasticPort,
							Protocol:      "TCP",
						},
					},
					Env: envVarList(hc),
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "humio-data",
							MountPath: humioDataPath,
						},
						{
							Name:      "humio-tmp",
							MountPath: humioDataTmpPath,
							ReadOnly:  false,
						},
						{
							Name:      "shared",
							MountPath: sharedPath,
							ReadOnly:  true,
						},
						{
							Name:      "tmp",
							MountPath: tmpPath,
							ReadOnly:  false,
						},
					},
					ReadinessProbe: &corev1.Probe{
						Handler: corev1.Handler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/api/v1/status",
								Port: intstr.IntOrString{IntVal: 8080},
							},
						},
						InitialDelaySeconds: 30,
						PeriodSeconds:       5,
						TimeoutSeconds:      2,
						SuccessThreshold:    1,
						FailureThreshold:    10,
					},
					LivenessProbe: &corev1.Probe{
						Handler: corev1.Handler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/api/v1/status",
								Port: intstr.IntOrString{IntVal: 8080},
							},
						},
						InitialDelaySeconds: 30,
						PeriodSeconds:       5,
						TimeoutSeconds:      2,
						SuccessThreshold:    1,
						FailureThreshold:    10,
					},
					Resources:       podResourcesOrDefault(hc),
					SecurityContext: containerSecurityContextOrDefault(hc),
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         "shared",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
				{
					Name:         "tmp",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
				{
					Name:         "humio-tmp",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
				{
					Name: "init-service-account-secret",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  initServiceAccountSecretName(hc),
							DefaultMode: &mode,
						},
					},
				},
				{
					Name: "auth-service-account-secret",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  authServiceAccountSecretName(hc),
							DefaultMode: &mode,
						},
					},
				},
			},
			Affinity:        affinityOrDefault(hc),
			SecurityContext: podSecurityContextOrDefault(hc),
		},
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         "humio-data",
		VolumeSource: dataVolumeSource,
	})

	idx, err := kubernetes.GetContainerIndexByName(pod, "humio")
	if err != nil {
		return &corev1.Pod{}, err
	}
	if envVarHasValue(pod.Spec.Containers[idx].Env, "AUTHENTICATION_METHOD", "saml") {
		idx, err := kubernetes.GetContainerIndexByName(pod, "humio")
		if err != nil {
			return &corev1.Pod{}, err
		}
		pod.Spec.Containers[idx].Env = append(pod.Spec.Containers[idx].Env, corev1.EnvVar{
			Name:  "SAML_IDP_CERTIFICATE",
			Value: fmt.Sprintf("/var/lib/humio/idp-certificate-secret/%s", idpCertificateFilename),
		})
		pod.Spec.Containers[idx].VolumeMounts = append(pod.Spec.Containers[idx].VolumeMounts, corev1.VolumeMount{
			Name:      "idp-cert-volume",
			ReadOnly:  true,
			MountPath: "/var/lib/humio/idp-certificate-secret",
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "idp-cert-volume",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  idpCertificateSecretNameOrDefault(hc),
					DefaultMode: &mode,
				},
			},
		})
	}

	if hc.Spec.HumioServiceAccountName != "" {
		pod.Spec.ServiceAccountName = hc.Spec.HumioServiceAccountName
	}

	if extraKafkaConfigsOrDefault(hc) != "" {
		pod.Spec.Containers[idx].Env = append(pod.Spec.Containers[idx].Env, corev1.EnvVar{
			Name:  "EXTRA_KAFKA_CONFIGS_FILE",
			Value: fmt.Sprintf("/var/lib/humio/extra-kafka-configs-configmap/%s", extraKafkaPropertiesFilename),
		})
		pod.Spec.Containers[idx].VolumeMounts = append(pod.Spec.Containers[idx].VolumeMounts, corev1.VolumeMount{
			Name:      "extra-kafka-configs",
			ReadOnly:  true,
			MountPath: "/var/lib/humio/extra-kafka-configs-configmap",
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "extra-kafka-configs",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: extraKafkaConfigsConfigMapName(hc),
					},
					DefaultMode: &mode,
				},
			},
		})
	}

	if hc.Spec.ImagePullPolicy != "" {
		for idx := range pod.Spec.InitContainers {
			pod.Spec.InitContainers[idx].ImagePullPolicy = hc.Spec.ImagePullPolicy
		}
		for idx := range pod.Spec.Containers {
			pod.Spec.Containers[idx].ImagePullPolicy = hc.Spec.ImagePullPolicy
		}
	}

	for _, volumeMount := range extraHumioVolumeMountsOrDefault(hc) {
		for _, existingVolumeMount := range pod.Spec.Containers[idx].VolumeMounts {
			if existingVolumeMount.Name == volumeMount.Name {
				return &corev1.Pod{}, fmt.Errorf("extraHumioVolumeMount conflicts with existing name: %s", existingVolumeMount.Name)
			}
			if strings.HasPrefix(existingVolumeMount.MountPath, volumeMount.MountPath) {
				return &corev1.Pod{}, fmt.Errorf("extraHumioVolumeMount conflicts with existing mount path: %s", existingVolumeMount.MountPath)
			}
		}
		pod.Spec.Containers[idx].VolumeMounts = append(pod.Spec.Containers[idx].VolumeMounts, volumeMount)
	}
	for _, volume := range extraVolumesOrDefault(hc) {
		for _, existingVolume := range pod.Spec.Volumes {
			if existingVolume.Name == volume.Name {
				return &corev1.Pod{}, fmt.Errorf("extraVolume conflicts with existing name: %s", existingVolume.Name)
			}
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
	}

	return &pod, nil
}

func volumeSource(hc *corev1alpha1.HumioCluster, podList []corev1.Pod, pvcList []corev1.PersistentVolumeClaim) (corev1.VolumeSource, error) {
	emptyDataVolume := corev1.VolumeSource{}

	if pvcsEnabled(hc) && !reflect.DeepEqual(hc.Spec.DataVolumeSource, emptyDataVolume) {
		return corev1.VolumeSource{}, fmt.Errorf("cannot have both dataVolumePersistentVolumeClaimSpecTemplate and dataVolumeSource defined")
	}
	if pvcsEnabled(hc) {
		pvcName, err := findNextAvailablePvc(pvcList, podList)
		if err != nil {
			return corev1.VolumeSource{}, err
		}
		return dataVolumePersistentVolumeClaimSpecTemplateOrDefault(hc, pvcName), nil
	}
	return dataVolumeSourceOrDefault(hc), nil
}

func envVarHasValue(envVars []corev1.EnvVar, key string, value string) bool {
	for _, envVar := range envVars {
		if envVar.Name == key && envVar.Value == value {
			return true
		}
	}
	return false
}

// podSpecAsSHA256 looks at the pod spec minus known nondeterministic fields and returns a sha256 hash of the spec
func podSpecAsSHA256(hc *corev1alpha1.HumioCluster, pod corev1.Pod) string {
	sanitizedVolumes := make([]corev1.Volume, len(pod.Spec.Volumes))
	emptyPersistentVolumeClaim := corev1.PersistentVolumeClaimVolumeSource{}

	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "humio-data" && !reflect.DeepEqual(volume.PersistentVolumeClaim, emptyPersistentVolumeClaim) {
			sanitizedVolumes = append(sanitizedVolumes, corev1.Volume{
				Name:         "humio-data",
				VolumeSource: dataVolumeSourceOrDefault(hc),
			})
		} else {
			sanitizedVolumes = append(sanitizedVolumes, volume)
		}
	}
	pod.Spec.Volumes = sanitizedVolumes
	return helpers.AsSHA256(pod.Spec)
}

func (r *ReconcileHumioCluster) createPod(ctx context.Context, hc *corev1alpha1.HumioCluster, foundPodList []corev1.Pod) error {
	pvcList, err := r.pvcList(hc)
	if err != nil {
		r.logger.Errorf("problem getting pvc list: %s", err)
		return err
	}
	r.logger.Debugf("attempting to get volume source, pvc count is %d, pod count is %d", len(pvcList), len(foundPodList))
	volumeSource, err := volumeSource(hc, foundPodList, pvcList)
	if err != nil {
		r.logger.Errorf("unable to construct data volume source for HumioCluster: %s", err)
		return err

	}
	pod, err := constructPod(hc, volumeSource)
	if err != nil {
		r.logger.Errorf("unable to construct pod for HumioCluster: %s", err)
		return err
	}
	r.logger.Debugf("pod %s will use volume source %+v", pod.Name, volumeSource)
	pod.Annotations[podHashAnnotation] = podSpecAsSHA256(hc, *pod)
	if err := controllerutil.SetControllerReference(hc, pod, r.scheme); err != nil {
		r.logger.Errorf("could not set controller reference: %s", err)
		return err
	}

	podRevision, err := r.getHumioClusterPodRevision(hc)
	if err != nil {
		return err
	}
	r.logger.Infof("setting pod %s revision to %d", pod.Name, podRevision)
	err = r.setPodRevision(pod, podRevision)
	if err != nil {
		return err
	}

	r.logger.Infof("creating pod %s", pod.Name)
	err = r.client.Create(ctx, pod)
	if err != nil {
		return err
	}
	r.logger.Infof("successfully created pod %s for HumioCluster %s", pod.Name, hc.Name)
	return nil
}

func (r *ReconcileHumioCluster) waitForNewPod(hc *corev1alpha1.HumioCluster, expectedPodCount int) error {
	for i := 0; i < 30; i++ {
		latestPodList, err := kubernetes.ListPods(r.client, hc.Namespace, kubernetes.MatchingLabelsForHumio(hc.Name))
		if err != nil {
			return err
		}
		r.logger.Infof("validating new pod was created. expected pod count %d, current pod count %d", expectedPodCount, len(latestPodList))
		if len(latestPodList) >= expectedPodCount {
			return nil
		}
		time.Sleep(time.Second * 1)
	}
	return fmt.Errorf("timed out waiting to validate new pod was created")
}

func (r *ReconcileHumioCluster) podsMatch(hc *corev1alpha1.HumioCluster, pod corev1.Pod, desiredPod corev1.Pod) (bool, error) {
	if _, ok := pod.Annotations[podHashAnnotation]; !ok {
		r.logger.Errorf("did not find annotation with pod hash")
		return false, fmt.Errorf("did not find annotation with pod hash")
	}
	if _, ok := pod.Annotations[podRevisionAnnotation]; !ok {
		r.logger.Errorf("did not find annotation with pod revision")
		return false, fmt.Errorf("did not find annotation with pod revision")
	}
	var specMatches bool
	var revisionMatches bool

	desiredPodHash := podSpecAsSHA256(hc, desiredPod)
	existingPodRevision, err := r.getHumioClusterPodRevision(hc)
	if err != nil {
		return false, err
	}
	err = r.setPodRevision(&desiredPod, existingPodRevision)
	if err != nil {
		return false, err
	}
	if pod.Annotations[podHashAnnotation] == desiredPodHash {
		specMatches = true
	}
	if pod.Annotations[podRevisionAnnotation] == desiredPod.Annotations[podRevisionAnnotation] {
		revisionMatches = true
	}
	if !specMatches {
		r.logger.Infof("pod annotation %s does not match desired pod: got %+v, expected %+v", podHashAnnotation, pod.Annotations[podHashAnnotation], desiredPod.Annotations[podHashAnnotation])
		return false, nil
	}
	if !revisionMatches {
		r.logger.Infof("pod annotation %s does not match desired pod: got %+v, expected %+v", podRevisionAnnotation, pod.Annotations[podRevisionAnnotation], desiredPod.Annotations[podRevisionAnnotation])
		return false, nil
	}
	return true, nil
}

func (r *ReconcileHumioCluster) getRestartPolicyFromPodInspection(pod, desiredPod corev1.Pod) (string, error) {
	humioContainerIdx, err := kubernetes.GetContainerIndexByName(pod, "humio")
	if err != nil {
		return "", err
	}
	desiredHumioContainerIdx, err := kubernetes.GetContainerIndexByName(desiredPod, "humio")
	if err != nil {
		return "", err
	}
	if pod.Spec.Containers[humioContainerIdx].Image != desiredPod.Spec.Containers[desiredHumioContainerIdx].Image {
		return PodRestartPolicyRecreate, nil
	}
	return PodRestartPolicyRolling, nil
}

func (r *ReconcileHumioCluster) podsReady(foundPodList []corev1.Pod) (int, int) {
	var podsReadyCount int
	var podsNotReadyCount int
	for _, pod := range foundPodList {
		podsNotReadyCount++
		// pods that were just deleted may still have a status of Ready, but we should not consider them ready
		if pod.DeletionTimestamp == nil {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == "Ready" {
					if condition.Status == "True" {
						r.logger.Debugf("pod %s is ready", pod.Name)
						podsReadyCount++
						podsNotReadyCount--
					} else {
						r.logger.Debugf("pod %s is not ready", pod.Name)
					}
				}
			}
		}
	}
	return podsReadyCount, podsNotReadyCount
}

func (r *ReconcileHumioCluster) getPodDesiredLifecyleState(hc *corev1alpha1.HumioCluster, foundPodList []corev1.Pod) (podLifecycleState, error) {
	for _, pod := range foundPodList {
		// only consider pods not already being deleted
		if pod.DeletionTimestamp == nil {
			// if pod spec differs, we want to delete it
			// use dataVolumeSourceOrDefault() to get either the volume source or an empty volume source in the case
			// we are using pvcs. this is to avoid doing the pvc lookup and we do not compare pvcs when doing a sha256
			// hash of the pod spec
			desiredPod, err := constructPod(hc, dataVolumeSourceOrDefault(hc))
			if err != nil {
				r.logger.Errorf("could not construct pod: %s", err)
				return podLifecycleState{}, err
			}

			podsMatchTest, err := r.podsMatch(hc, pod, *desiredPod)
			if err != nil {
				r.logger.Errorf("failed to check if pods match %s", err)
			}
			if !podsMatchTest {
				// TODO: figure out if we should only allow upgrades and not downgrades
				restartPolicy, err := r.getRestartPolicyFromPodInspection(pod, *desiredPod)
				if err != nil {
					r.logger.Errorf("could not get restart policy for HumioCluster: %s", err)
					return podLifecycleState{}, err
				}
				return podLifecycleState{
					pod:           pod,
					restartPolicy: restartPolicy,
					delete:        true,
				}, err
			}
		} else {
			return podLifecycleState{}, nil
		}
	}
	return podLifecycleState{}, nil
}