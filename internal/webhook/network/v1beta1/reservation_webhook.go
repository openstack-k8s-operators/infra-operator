/*
Copyright 2025.

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

package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	networkv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var reservationlog = logf.Log.WithName("reservation-resource")

// SetupReservationWebhookWithManager registers the webhook for Reservation in the manager.
func SetupReservationWebhookWithManager(mgr ctrl.Manager) error {
	// Set the webhook client for use in validation functions
	if err := networkv1beta1.SetWebhookClient(mgr.GetClient()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&networkv1beta1.Reservation{}).
		WithValidator(&ReservationCustomValidator{}).
		WithDefaulter(&ReservationCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-network-openstack-org-v1beta1-reservation,mutating=true,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=reservations,verbs=create;update,versions=v1beta1,name=mreservation-v1beta1.kb.io,admissionReviewVersions=v1

// ReservationCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Reservation when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ReservationCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &ReservationCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Reservation.
func (d *ReservationCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	reservation, ok := obj.(*networkv1beta1.Reservation)

	if !ok {
		return fmt.Errorf("expected an Reservation object but got %T", obj)
	}
	reservationlog.Info("Defaulting for Reservation", "name", reservation.GetName())

	reservation.Default()

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-reservation,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=reservations,verbs=create;update,versions=v1beta1,name=vreservation-v1beta1.kb.io,admissionReviewVersions=v1

// ReservationCustomValidator struct is responsible for validating the Reservation resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ReservationCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &ReservationCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Reservation.
func (v *ReservationCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	reservation, ok := obj.(*networkv1beta1.Reservation)
	if !ok {
		return nil, fmt.Errorf("expected a Reservation object but got %T", obj)
	}
	reservationlog.Info("Validation for Reservation upon creation", "name", reservation.GetName())

	return reservation.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Reservation.
func (v *ReservationCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	reservation, ok := newObj.(*networkv1beta1.Reservation)
	if !ok {
		return nil, fmt.Errorf("expected a Reservation object for the newObj but got %T", newObj)
	}
	reservationlog.Info("Validation for Reservation upon update", "name", reservation.GetName())

	return reservation.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Reservation.
func (v *ReservationCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	reservation, ok := obj.(*networkv1beta1.Reservation)
	if !ok {
		return nil, fmt.Errorf("expected a Reservation object but got %T", obj)
	}
	reservationlog.Info("Validation for Reservation upon deletion", "name", reservation.GetName())

	return reservation.ValidateDelete()
}
