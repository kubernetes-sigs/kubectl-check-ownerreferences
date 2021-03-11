/*
Copyright 2020 The Kubernetes Authors.

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

package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	klog "k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/pager"
)

// VerifyGCOptions contains options controlling how the verify task is run
type VerifyGCOptions struct {
	DiscoveryClient discovery.DiscoveryInterface
	MetadataClient  metadata.Interface
	Output          string
	Stderr          io.Writer
	Stdout          io.Writer
}

// Validate ensures the specified options are valid
func (v *VerifyGCOptions) Validate() error {
	if v.DiscoveryClient == nil {
		return fmt.Errorf("discovery client is required")
	}
	if v.MetadataClient == nil {
		return fmt.Errorf("metadata client is required")
	}
	if v.Stderr == nil {
		return fmt.Errorf("stderr is required")
	}
	if v.Stdout == nil {
		return fmt.Errorf("stdout is required")
	}
	if v.Output != "" && v.Output != "json" {
		return fmt.Errorf("invalid output format, only '' and 'json' are supported: %v", v.Output)
	}
	return nil
}

// Run executes the verify operation
func (v *VerifyGCOptions) Run() error {
	errorCount := 0
	warningCount := 0

	// set up REST mapper
	gvDiscoveryFailures := map[schema.GroupVersion]error{}
	groupDiscoveryError := &discovery.ErrGroupDiscoveryFailed{}
	allGroupResources, err := restmapper.GetAPIGroupResources(v.DiscoveryClient)
	if errors.As(err, &groupDiscoveryError) {
		// tolerate partial discovery
		for failedGV, err := range groupDiscoveryError.Groups {
			if _, alreadyFailed := gvDiscoveryFailures[failedGV]; !alreadyFailed {
				gvDiscoveryFailures[failedGV] = err
				warningCount++
				fmt.Fprintf(v.Stderr, "warning: could not discover resources in %s: %v", failedGV, err.Error())
			}
		}
	} else if err != nil {
		return err
	}
	restMapper := restmapper.NewDiscoveryRESTMapper(allGroupResources)

	// get preferred versions of GC-able resources
	preferredResources, err := discovery.ServerPreferredResources(v.DiscoveryClient)
	if errors.As(err, &groupDiscoveryError) {
		// tolerate partial discovery
		for failedGV, err := range groupDiscoveryError.Groups {
			if _, alreadyFailed := gvDiscoveryFailures[failedGV]; !alreadyFailed {
				gvDiscoveryFailures[failedGV] = err
				warningCount++
				fmt.Fprintf(v.Stderr, "warning: could not discover resources in %s: %v", failedGV, err.Error())
			}
		}
	} else if err != nil {
		return err
	}
	gcResources := discovery.FilteredBy(discovery.SupportsAllVerbs{Verbs: []string{"list", "get", "delete"}}, preferredResources)
	gvrMap, err := discovery.GroupVersionResources(gcResources)
	if err != nil {
		return err
	}
	gvrs := []schema.GroupVersionResource{}
	for gvr := range gvrMap {
		gvrs = append(gvrs, gvr)
	}
	sort.Slice(gvrs, func(i, j int) bool {
		if gvrs[i].Group != gvrs[j].Group {
			return gvrs[i].Group < gvrs[j].Group
		}
		if gvrs[i].Version != gvrs[j].Version {
			return gvrs[i].Version < gvrs[j].Version
		}
		return gvrs[i].Resource < gvrs[j].Resource
	})

	grListErrors := map[schema.GroupResource]error{}

	// fetch all resources
	// TODO: scope to just fetching some resources, or some namespaces
	byGVR := map[schema.GroupVersionResource][]*metav1.PartialObjectMetadata{}
	byUID := map[types.UID][]*metav1.PartialObjectMetadata{}
	for _, gvr := range gvrs {
		// reverse-lookup the kind for this resource to fill in individual items
		gvk, _ := restMapper.KindFor(gvr)

		if klog.V(2).Enabled() {
			fmt.Fprintf(v.Stderr, "fetching %v, %v\n", gvr.GroupVersion().String(), gvr.Resource)
		}
		pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			list, err := v.MetadataClient.Resource(gvr).List(ctx, opts)
			if err != nil {
				warningCount++
				fmt.Fprintf(v.Stderr, "warning: could not list %v: %v\n", gvr, err.Error())
				grListErrors[gvr.GroupResource()] = err
			} else if klog.V(3).Enabled() {
				fmt.Fprintf(v.Stderr, "got %s\n", pluralize(len(list.Items), "item", "items"))
			}
			return list, err
		}).EachListItem(context.Background(), metav1.ListOptions{}, func(object runtime.Object) error {
			item, ok := object.(*metav1.PartialObjectMetadata)
			if !ok {
				return fmt.Errorf("expected type *metav1.PartialObjectMetadata, got type %T", item)
			}
			if item.APIVersion == "" && item.Kind == "" && !gvk.Empty() {
				item.APIVersion = gvk.GroupVersion().String()
				item.Kind = gvk.Kind
			}
			byUID[item.UID] = append(byUID[item.UID], item)
			byGVR[gvr] = append(byGVR[gvr], item)
			return nil
		})
	}

	tabwriter := printers.GetNewTabWriter(v.Stdout)
	initialized := false
	var outputRefMessage func(gvr schema.GroupVersionResource, item *metav1.PartialObjectMetadata, ownerRef metav1.OwnerReference, level string, msg string)
	if v.Output == "" {
		outputRefMessage = func(gvr schema.GroupVersionResource, item *metav1.PartialObjectMetadata, ownerRef metav1.OwnerReference, level string, msg string) {
			if level == levelError {
				errorCount++
			} else {
				warningCount++
			}
			if !initialized {
				initialized = true
				tabwriter.Write([]byte("GROUP\tRESOURCE\tNAMESPACE\tNAME\tOWNER_UID\tLEVEL\tMESSAGE\n"))
			}
			tabwriter.Write([]byte(
				strings.Join([]string{
					gvr.Group, gvr.Resource, item.Namespace, item.Name, string(ownerRef.UID), level, msg,
				}, "\t") + "\n",
			))
		}
	} else if v.Output == "json" {
		outputRefMessage = func(gvr schema.GroupVersionResource, item *metav1.PartialObjectMetadata, ownerRef metav1.OwnerReference, level string, msg string) {
			json.NewEncoder(v.Stdout).Encode(invalidReference{
				Resource:       metav1.GroupVersionResource{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource},
				Kind:           metav1.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: item.Kind},
				Namespace:      item.Namespace,
				Name:           item.Name,
				OwnerReference: ownerRef,
				Level:          level,
				Message:        msg,
			})
		}
	}

	// iterate over all resource types
	for _, gvr := range gvrs {
		// iterate over all items
		for _, child := range byGVR[gvr] {
			// iterate over all owners
			for _, ownerRef := range child.OwnerReferences {
				// resolve REST info
				ownerGV, err := schema.ParseGroupVersion(ownerRef.APIVersion)
				if err != nil {
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("invalid owner apiVersion %s: %v", ownerRef.APIVersion, err.Error()))
					continue
				}
				ownerGVK := ownerGV.WithKind(ownerRef.Kind)
				mapping, err := restMapper.RESTMapping(ownerGVK.GroupKind(), ownerGVK.Version)
				if err != nil {
					if discoveryErr, discoveryFailed := gvDiscoveryFailures[ownerGV]; discoveryFailed {
						// warn on discovery failure for the referenced apiVersion
						outputRefMessage(gvr, child, ownerRef, levelWarning, fmt.Sprintf("failed resolving resources for %s: %v", ownerRef.APIVersion, discoveryErr.Error()))
						continue
					}
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("cannot resolve owner apiVersion/kind: %v", err))
					continue
				}
				ownerGR := mapping.Resource.GroupResource()
				// ownerRef apiVersion/kind is namespaced, child is cluster-scoped
				if mapping.Scope.Name() == meta.RESTScopeNameNamespace && child.Namespace == "" {
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("cannot reference namespaced type as owner (apiVersion=%s,kind=%s)", ownerGVK.GroupVersion().String(), ownerGVK.Kind))
					continue
				}

				// compare with actual objects we found with that uid
				actualOwners := byUID[ownerRef.UID]
				if len(actualOwners) == 0 {
					if _, listFailed := grListErrors[ownerGR]; listFailed {
						// warn on missing owners if failed to list owner resource
						outputRefMessage(gvr, child, ownerRef, levelWarning, fmt.Sprintf("could not list parent resource %v", ownerGR))
						continue
					}
					outputRefMessage(gvr, child, ownerRef, levelError, "no object found for uid")
					continue
				}

				var (
					namespaceOk     = false
					actualNamespace = ""

					nameOk     = false
					actualName = ""

					groupKindOk = false
					actualGVK   = schema.GroupVersionKind{}
				)
				for _, actualOwner := range actualOwners {
					if actualOwner.Name == ownerRef.Name {
						nameOk = true
					} else {
						actualName = actualOwner.Name
					}

					if actualOwner.Namespace == "" || actualOwner.Namespace == child.Namespace {
						namespaceOk = true
					} else {
						actualNamespace = actualOwner.Namespace
					}

					if actualOwner.APIVersion == "" || actualOwner.Kind == "" {
						groupKindOk = true
					} else {
						actualOwnerGV, _ := schema.ParseGroupVersion(actualOwner.APIVersion)
						if actualOwner.Kind == ownerRef.Kind && actualOwnerGV.Group == ownerGV.Group {
							groupKindOk = true
						} else if strings.ToLower(actualOwner.Kind) == ownerRef.Kind && actualOwnerGV.Group == ownerGV.Group {
							// RESTMapper tolerates an all-lowercase kind as input to the lookup
							// https://github.com/kubernetes/kubernetes/blob/release-1.20/staging/src/k8s.io/client-go/restmapper/discovery.go#L114
							groupKindOk = true
						} else {
							actualGVK = actualOwnerGV.WithKind(actualOwner.Kind)
						}
					}
				}

				if !namespaceOk {
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("child namespace does not match owner namespace (%s)", actualNamespace))
					continue
				}
				if !nameOk {
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("ownerReference name (%s) does not match owner name (%s)", ownerRef.Name, actualName))
					continue
				}
				if !groupKindOk {
					outputRefMessage(gvr, child, ownerRef, levelError, fmt.Sprintf("ownerReference group/kind (%s/%s) does not match owner group/kind (%s/%s)", ownerGV.Group, ownerRef.Kind, actualGVK.Group, actualGVK.Kind))
					continue
				}
			}
		}
		// flush after each type
		tabwriter.Flush()
	}

	if errorCount > 0 || warningCount > 0 {
		fmt.Fprintf(v.Stderr, "%s, %s\n", pluralize(errorCount, "error", "errors"), pluralize(warningCount, "warning", "warnings"))
	} else {
		fmt.Fprintf(v.Stderr, "No invalid ownerReferences found\n")
	}
	return nil
}

var (
	levelError   = "Error"
	levelWarning = "Warning"
)

type invalidReference struct {
	Resource       metav1.GroupVersionResource `json:"resource"`
	Kind           metav1.GroupVersionKind     `json:"kind"`
	Namespace      string                      `json:"namespace"`
	Name           string                      `json:"name"`
	OwnerReference metav1.OwnerReference       `json:"ownerReference"`
	Level          string                      `json:"level"`
	Message        string                      `json:"message"`
}

func pluralize(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}
