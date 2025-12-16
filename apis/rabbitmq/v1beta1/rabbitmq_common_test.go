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

import "testing"

func TestDefaultRabbitMqConfig(t *testing.T) {
	tests := []struct {
		name               string
		config             *RabbitMqConfig
		defaultClusterName string
		wantClusterName    string
		wantUser           string
		wantVhost          string
	}{
		{
			name: "should set Cluster when it's empty and defaultClusterName is provided",
			config: &RabbitMqConfig{
				User:  "testuser",
				Vhost: "testvhost",
				// Cluster is empty
			},
			defaultClusterName: "default-rabbitmq",
			wantClusterName:    "default-rabbitmq",
			wantUser:           "testuser",
			wantVhost:          "testvhost",
		},
		{
			name: "should not override Cluster when it's already set",
			config: &RabbitMqConfig{
				User:    "testuser",
				Vhost:   "testvhost",
				Cluster: "existing-cluster",
			},
			defaultClusterName: "default-rabbitmq",
			wantClusterName:    "existing-cluster",
			wantUser:           "testuser",
			wantVhost:          "testvhost",
		},
		{
			name: "should not set Cluster when defaultClusterName is empty",
			config: &RabbitMqConfig{
				User:  "testuser",
				Vhost: "testvhost",
				// Cluster is empty
			},
			defaultClusterName: "",
			wantClusterName:    "",
			wantUser:           "testuser",
			wantVhost:          "testvhost",
		},
		{
			name: "should not set Cluster when both Cluster and defaultClusterName are empty",
			config: &RabbitMqConfig{
				User:  "testuser",
				Vhost: "testvhost",
				// Cluster is empty
			},
			defaultClusterName: "",
			wantClusterName:    "",
			wantUser:           "testuser",
			wantVhost:          "testvhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy of config to avoid modifying the original
			configCopy := *tt.config
			config := &configCopy

			DefaultRabbitMqConfig(config, tt.defaultClusterName)

			if config.Cluster != tt.wantClusterName {
				t.Errorf("Cluster = %q, want %q", config.Cluster, tt.wantClusterName)
			}
			if config.User != tt.wantUser {
				t.Errorf("User = %q, want %q", config.User, tt.wantUser)
			}
			if config.Vhost != tt.wantVhost {
				t.Errorf("Vhost = %q, want %q", config.Vhost, tt.wantVhost)
			}
		})
	}
}
