/*
Copyright 2022.

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

package functional_test

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("BGPConfiguration controller", func() {
	var bgpcfgName types.NamespacedName
	var meallbFRRCfgName types.NamespacedName
	frrCfgNamespace := "metallb-system"

	When("a default BGPConfiguration gets created", func() {
		BeforeEach(func() {
			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(""))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()
			DeferCleanup(th.DeleteInstance, bgpcfg)
		})

		It("should have created a BGPConfiguration with default FRRConfigurationNamespace", func() {
			Eventually(func(g Gomega) {
				bgpcfg := GetBGPConfiguration(bgpcfgName)
				g.Expect(bgpcfg).To(Not(BeNil()))
				g.Expect(bgpcfg.Spec.FRRConfigurationNamespace).To(Equal(frrCfgNamespace))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a BGPConfiguration CR is deleted, finalizer logic removes owned FRRConfiguration objects", func() {
		var podFrrName types.NamespacedName
		var podName types.NamespacedName
		var metallbNS *corev1.Namespace
		var bgpcfg client.Object

		BeforeEach(func() {
			metallbNS = th.CreateNamespace(frrCfgNamespace + "-" + namespace)
			// create a FRR configuration for a node
			meallbFRRCfgName = types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-0"}
			meallbFRRCfg := CreateFRRConfiguration(meallbFRRCfgName, GetMetalLBFRRConfigurationSpec("worker-0"))
			Expect(meallbFRRCfg).To(Not(BeNil()))

			// create a nad config with gateway
			nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())

			bgpcfg = CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podName = types.NamespacedName{Namespace: namespace, Name: uuid.New().String()}
			// create pod without NAD annotation
			th.CreatePod(podName, map[string]string{}, GetPodSpec("worker-0"))
			th.SimulatePodPhaseRunning(podName)

			podFrrName.Name = podName.Namespace + "-" + podName.Name
			podFrrName.Namespace = metallbNS.Name

			DeferCleanup(th.DeleteInstance, nad)
			DeferCleanup(th.DeleteInstance, meallbFRRCfg)
		})

		It("should remove all owned FRRConfiguration objects after deletion", func() {
			pod := th.GetPod(podName)
			Expect(pod).To(Not(BeNil()))

			// Delete the BGPConfiguration CR.
			th.DeleteInstance(bgpcfg)

			// validate that the pod frr cfg is gone
			frr := &frrk8sv1.FRRConfiguration{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, podFrrName, frr)).Should(Not(Succeed()))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("A pod with NAD gets created but node FRR reference configuration missing", func() {
		var podFrrName types.NamespacedName
		var podName types.NamespacedName
		var metallbNS *corev1.Namespace

		BeforeEach(func() {
			metallbNS = th.CreateNamespace(frrCfgNamespace + "-" + namespace)

			// create a nad config with gateway
			nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())

			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podName = types.NamespacedName{Namespace: namespace, Name: uuid.New().String()}
			// create pod with NAD annotation
			th.CreatePod(podName, GetPodAnnotation(namespace), GetPodSpec("worker-0"))
			th.SimulatePodPhaseRunning(podName)

			podFrrName.Name = podName.Namespace + "-" + podName.Name
			podFrrName.Namespace = frrCfgNamespace

			DeferCleanup(th.DeleteInstance, bgpcfg)
			DeferCleanup(th.DeleteInstance, nad)
		})

		It("should NOT have created a FRRConfiguration for the pod", func() {
			pod := th.GetPod(podName)
			Expect(pod).To(Not(BeNil()))

			frr := &frrk8sv1.FRRConfiguration{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, podFrrName, frr)).Should(Not(Succeed()))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a pod gets created", func() {
		var podFrrName types.NamespacedName
		var podName types.NamespacedName
		var metallbNS *corev1.Namespace

		BeforeEach(func() {
			metallbNS = th.CreateNamespace(frrCfgNamespace + "-" + namespace)
			// create a FRR configuration for a node
			meallbFRRCfgName = types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-0"}
			meallbFRRCfg := CreateFRRConfiguration(meallbFRRCfgName, GetMetalLBFRRConfigurationSpec("worker-0"))
			Expect(meallbFRRCfg).To(Not(BeNil()))

			// TODO test without GW?
			// create a nad config with gateway
			nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())

			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podName = types.NamespacedName{Namespace: namespace, Name: uuid.New().String()}
			// create pod without NAD annotation
			th.CreatePod(podName, map[string]string{}, GetPodSpec("worker-0"))
			th.SimulatePodPhaseRunning(podName)

			podFrrName.Name = podName.Namespace + "-" + podName.Name
			podFrrName.Namespace = metallbNS.Name

			DeferCleanup(th.DeleteInstance, bgpcfg)
			DeferCleanup(th.DeleteInstance, nad)
			DeferCleanup(th.DeleteInstance, meallbFRRCfg)
		})

		It("should NOT have created a FRRConfiguration for the pod", func() {
			pod := th.GetPod(podName)
			Expect(pod).To(Not(BeNil()))

			frr := &frrk8sv1.FRRConfiguration{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, podFrrName, frr)).Should(Not(Succeed()))
			}, timeout, interval).Should(Succeed())
		})

		When("NAD annotation gets added to the pod", func() {
			BeforeEach(func() {
				pod := th.GetPod(podName)
				Expect(pod).To(Not(BeNil()))

				pod.Annotations = GetPodAnnotation(namespace)
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Update(ctx, pod)).Should(Succeed())
				}, timeout, interval).Should(Succeed())
			})

			It("should have created a FRRConfiguration for the pod", func() {
				pod := th.GetPod(podName)
				Expect(pod).To(Not(BeNil()))

				podFrrName := podName.Namespace + "-" + podName.Name
				Eventually(func(g Gomega) {
					frr := GetFRRConfiguration(types.NamespacedName{Namespace: metallbNS.Name, Name: podFrrName})
					g.Expect(frr).To(Not(BeNil()))
					g.Expect(frr.Spec.BGP.Routers[0].Prefixes[0]).To(Equal("172.17.0.40/32"))
				}, timeout, interval).Should(Succeed())

			})
		})

		When("another pod with NAD annotation gets created", func() {
			var podName types.NamespacedName

			BeforeEach(func() {
				podName = types.NamespacedName{Namespace: namespace, Name: uuid.New().String()}
				// create pod with NAD annotation
				th.CreatePod(podName, GetPodAnnotation(namespace), GetPodSpec("worker-0"))
				th.SimulatePodPhaseRunning(podName)
			})

			It("should have created a FRRConfiguration for the pod2", func() {
				pod := th.GetPod(podName)
				Expect(pod).To(Not(BeNil()))

				podFrrName := podName.Namespace + "-" + podName.Name
				Eventually(func(g Gomega) {
					frr := GetFRRConfiguration(types.NamespacedName{Namespace: metallbNS.Name, Name: podFrrName})
					g.Expect(frr).To(Not(BeNil()))
					g.Expect(frr.Spec.BGP.Routers[0].Prefixes[0]).To(Equal("172.17.0.40/32"))
				}, timeout, interval).Should(Succeed())

			})
		})
	})

	When("a pod gets re-created on another node", func() {
		var podFrrNameList []types.NamespacedName
		var podNameList []types.NamespacedName

		BeforeEach(func() {
			metallbNS := th.CreateNamespace(frrCfgNamespace + "-" + namespace)
			// create a FRR configuration for 2 nodes
			meallbFRRCfgWorker0 := CreateFRRConfiguration(
				types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-0"},
				GetMetalLBFRRConfigurationSpec("worker-0"))
			Expect(meallbFRRCfgWorker0).To(Not(BeNil()))
			meallbFRRCfgWorker1 := CreateFRRConfiguration(
				types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-1"},
				GetMetalLBFRRConfigurationSpec("worker-1"))
			Expect(meallbFRRCfgWorker1).To(Not(BeNil()))

			// create a nad config with gateway
			nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())

			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podNameList = []types.NamespacedName{
				{Namespace: namespace, Name: "foo"},
				{Namespace: namespace, Name: "bar"},
			}

			for _, podName := range podNameList {
				// create pod with NAD annotation
				th.CreatePod(podName, GetPodAnnotation(namespace), GetPodSpec("worker-0"))
				th.SimulatePodPhaseRunning(podName)

				podFrrName := types.NamespacedName{
					Name:      podName.Namespace + "-" + podName.Name,
					Namespace: metallbNS.Name,
				}
				podFrrNameList = append(podFrrNameList, podFrrName)

				Eventually(func(g Gomega) {
					frr := GetFRRConfiguration(podFrrName)
					g.Expect(frr).To(Not(BeNil()))
				}, timeout, interval).Should(Succeed())
			}

			DeferCleanup(th.DeleteInstance, bgpcfg)
			DeferCleanup(th.DeleteInstance, nad)
			DeferCleanup(th.DeleteInstance, meallbFRRCfgWorker0)
			DeferCleanup(th.DeleteInstance, meallbFRRCfgWorker1)
		})

		It("should re-create/update the FRRConfiguration with the new nodeselector", func() {
			// delete pod foo
			pod := th.GetPod(podNameList[0])
			Expect(pod).To(Not(BeNil()))
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Delete(ctx, pod)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// validate that the corresponding frr cfg is gone
			frr := &frrk8sv1.FRRConfiguration{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, podFrrNameList[0], frr)).Should(Not(Succeed()))
			}, timeout, interval).Should(Succeed())

			// re-create pod on different node
			th.CreatePod(podNameList[0], GetPodAnnotation(namespace), GetPodSpec("worker-1"))
			th.SimulatePodPhaseRunning(podNameList[0])

			Eventually(func(g Gomega) {
				frr := GetFRRConfiguration(podFrrNameList[0])
				g.Expect(frr).To(Not(BeNil()))
				g.Expect(frr.Spec.NodeSelector.MatchLabels).Should(BeEquivalentTo(
					map[string]string{
						corev1.LabelHostname: "worker-1",
					}))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a pod with NAD gets deleted", func() {
		var podFrrNameList []types.NamespacedName
		var podNameList []types.NamespacedName

		BeforeEach(func() {
			metallbNS := th.CreateNamespace(frrCfgNamespace + "-" + namespace)
			// create a FRR configuration for a node
			meallbFRRCfgName = types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-0"}
			meallbFRRCfg := CreateFRRConfiguration(meallbFRRCfgName, GetMetalLBFRRConfigurationSpec("worker-0"))
			Expect(meallbFRRCfg).To(Not(BeNil()))

			// create a nad config with gateway
			nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())

			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podNameList = []types.NamespacedName{
				{Namespace: namespace, Name: "foo"},
				{Namespace: namespace, Name: "bar"},
			}

			for _, podName := range podNameList {
				// create pod with NAD annotation
				th.CreatePod(podName, GetPodAnnotation(namespace), GetPodSpec("worker-0"))
				th.SimulatePodPhaseRunning(podName)

				podFrrName := types.NamespacedName{
					Name:      podName.Namespace + "-" + podName.Name,
					Namespace: metallbNS.Name,
				}
				podFrrNameList = append(podFrrNameList, podFrrName)

				Eventually(func(g Gomega) {
					frr := GetFRRConfiguration(podFrrName)
					g.Expect(frr).To(Not(BeNil()))
				}, timeout, interval).Should(Succeed())
			}

			DeferCleanup(th.DeleteInstance, bgpcfg)
			DeferCleanup(th.DeleteInstance, nad)
			DeferCleanup(th.DeleteInstance, meallbFRRCfg)
		})

		It("should delete the FRRConfiguration when on pod gets deleted", func() {
			// delete pod foo
			pod := th.GetPod(podNameList[0])
			Expect(pod).To(Not(BeNil()))
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Delete(ctx, pod)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// validate that the corresponding frr cfg is gone
			frr := &frrk8sv1.FRRConfiguration{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, podFrrNameList[0], frr)).Should(Not(Succeed()))
			}, timeout, interval).Should(Succeed())
		})

		It("should delete all FRRConfiguration when all pod gets deleted", func() {
			// delete all pod
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.DeleteAllOf(
					ctx,
					&corev1.Pod{},
					client.InNamespace(podNameList[0].Namespace),
				)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			for idx := range podNameList {
				// validate that the frr cfgs are gone
				frr := &frrk8sv1.FRRConfiguration{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, podFrrNameList[idx], frr)).Should(Not(Succeed()))
				}, timeout, interval).Should(Succeed())
			}
		})
	})

	When("a pod with predictableip label gets created", func() {
		var podFrrName types.NamespacedName
		var podName types.NamespacedName
		var metallbNS *corev1.Namespace

		BeforeEach(func() {
			metallbNS = th.CreateNamespace(frrCfgNamespace + "-" + namespace)
			// create a FRR configuration for a node
			meallbFRRCfgName = types.NamespacedName{Namespace: metallbNS.Name, Name: "worker-0"}
			meallbFRRCfg := CreateFRRConfiguration(meallbFRRCfgName, GetMetalLBFRRConfigurationSpec("worker-0"))
			Expect(meallbFRRCfg).To(Not(BeNil()))

			bgpcfg := CreateBGPConfiguration(namespace, GetBGPConfigurationSpec(metallbNS.Name))
			bgpcfgName.Name = bgpcfg.GetName()
			bgpcfgName.Namespace = bgpcfg.GetNamespace()

			podName = types.NamespacedName{Namespace: namespace, Name: uuid.New().String()}
			// create pod with predictableip label but no NAD annotation
			podLabels := map[string]string{
				"predictableip": "172.67.0.102",
			}

			// Create pod directly with labels
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName.Name,
					Namespace: podName.Namespace,
					Labels:    podLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "foo",
							Image: "foo:latest",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
						},
					},
					TerminationGracePeriodSeconds: func() *int64 { i := int64(0); return &i }(),
					NodeName:                      "worker-0",
				},
			}
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			th.SimulatePodPhaseRunning(podName)

			podFrrName.Name = podName.Namespace + "-" + podName.Name
			podFrrName.Namespace = metallbNS.Name

			DeferCleanup(th.DeleteInstance, bgpcfg)
			DeferCleanup(th.DeleteInstance, meallbFRRCfg)
			DeferCleanup(th.DeleteInstance, pod)
		})

		It("should have created a FRRConfiguration for the pod with predictableip", func() {
			pod := th.GetPod(podName)
			Expect(pod).To(Not(BeNil()))
			Expect(pod.Labels["predictableip"]).To(Equal("172.67.0.102"))

			podFrrName := podName.Namespace + "-" + podName.Name
			Eventually(func(g Gomega) {
				frr := GetFRRConfiguration(types.NamespacedName{Namespace: metallbNS.Name, Name: podFrrName})
				g.Expect(frr).To(Not(BeNil()))
				g.Expect(frr.Spec.BGP.Routers[0].Prefixes).To(ContainElement("172.67.0.102/32"))
			}, timeout, interval).Should(Succeed())
		})

		When("NAD annotation gets added to the pod with predictableip", func() {
			BeforeEach(func() {
				// create a nad config with gateway
				nad := th.CreateNAD(types.NamespacedName{Namespace: namespace, Name: "internalapi"}, GetNADSpec())
				DeferCleanup(th.DeleteInstance, nad)

				pod := th.GetPod(podName)
				Expect(pod).To(Not(BeNil()))

				pod.Annotations = GetPodAnnotation(namespace)
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Update(ctx, pod)).Should(Succeed())
				}, timeout, interval).Should(Succeed())
			})

			It("should have created a FRRConfiguration with both NAD and predictableip prefixes", func() {
				pod := th.GetPod(podName)
				Expect(pod).To(Not(BeNil()))
				Expect(pod.Labels["predictableip"]).To(Equal("172.67.0.102"))

				podFrrName := podName.Namespace + "-" + podName.Name
				Eventually(func(g Gomega) {
					frr := GetFRRConfiguration(types.NamespacedName{Namespace: metallbNS.Name, Name: podFrrName})
					g.Expect(frr).To(Not(BeNil()))
					// Should have both the NAD IP and the predictable IP
					g.Expect(frr.Spec.BGP.Routers[0].Prefixes).To(ContainElement("172.17.0.40/32"))  // NAD IP
					g.Expect(frr.Spec.BGP.Routers[0].Prefixes).To(ContainElement("172.67.0.102/32")) // Predictable IP
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
