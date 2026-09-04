package compute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testVastWorkerImage  = "docker.io/vastai/kvm:test"
	testVastWorkerDiskGB = int64(200)
)

func testVastConfig(baseURL string) VastConfig {
	return VastConfig{
		APIKey:       "test-key",
		BaseURL:      baseURL,
		WorkerImage:  testVastWorkerImage,
		WorkerDiskGB: testVastWorkerDiskGB,
	}
}

func TestListOffersNormalizesVastBundle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/bundles/", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "on-demand", body["type"])
		require.Equal(t, map[string]any{"eq": true}, body["rentable"])
		require.Equal(t, map[string]any{"eq": true}, body["verified"])
		require.Equal(t, map[string]any{"eq": true}, body["vms_enabled"])
		require.Equal(t, map[string]any{"gte": float64(testVastWorkerDiskGB)}, body["disk_space"])
		require.Equal(t, float64(testVastWorkerDiskGB), body["allocated_storage"])
		require.Equal(t, map[string]any{"in": []any{"RTX A6000"}}, body["gpu_name"])
		require.Equal(t, map[string]any{"gte": float64(4)}, body["num_gpus"])
		require.NotContains(t, body, "q")
		_, _ = w.Write([]byte(`{"offers":[
			{"id":123,"gpu_name":"RTX A6000","num_gpus":8,"dph_total":10.5,"cpu_cores":64,"cpu_ram":524288,"disk_space":750.5,"geolocation":"US","rentable_count":2},
			{"id":124,"gpu_name":"RTX A6000","num_gpus":1,"dph_total":1.5,"cpu_cores":16,"cpu_ram":65536,"disk_space":750.5,"geolocation":"US","rentable_count":2}
		]}`))
	}))
	defer server.Close()

	client := NewVast(testVastConfig(server.URL))
	offers, err := client.ListOffers(context.Background(), OfferRequest{GPUs: []string{"A6000"}, GPUCount: 4})

	require.NoError(t, err)
	require.Len(t, offers, 1)
	require.Equal(t, "123", offers[0].ID)
	require.Equal(t, "vast", offers[0].Provider)
	require.Equal(t, "A6000", offers[0].GPU)
	require.Equal(t, uint32(8), offers[0].GPUCount)
	require.Equal(t, int64(524288), offers[0].MemoryMB)
	require.Equal(t, int64(750.5*1024), offers[0].StorageMB)
	require.Equal(t, DollarsToMicros(10.5), offers[0].HourlyCostMicros)
	require.Equal(t, uint32(2), offers[0].Available)
}

func TestCreateReservationConfiguresVastOnstart(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/asks/123/", r.URL.Path)
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_, _ = w.Write([]byte(`{"new_contract":"instance-123"}`))
	}))
	defer server.Close()

	client := NewVast(testVastConfig(server.URL))
	reservation, err := client.CreateReservation(context.Background(), ReservationRequest{
		PoolName:         "training",
		MachineID:        "machine-123",
		Name:             "beam-workspace-training-machine-123",
		Selector:         "training",
		Offer:            Offer{ID: "123", Provider: "vast", InstanceType: "machine-1", GPU: "H100", GPUCount: 8, HourlyCostMicros: DollarsToMicros(10.5)},
		TTL:              6 * time.Hour,
		Source:           SourceCLIReservation,
		BootstrapCommand: "curl -fsSL https://app.beam.cloud/install/agent | sudo bash -s -- --gateway https://gateway.beam.cloud --join-token token",
	})

	require.NoError(t, err)
	require.Equal(t, "instance-123", reservation.ID)
	require.Equal(t, "beam-workspace-training-machine-123", body["label"])
	require.Equal(t, "machine-123", body["client_id"])
	require.Equal(t, testVastWorkerImage, body["image"])
	require.Equal(t, float64(testVastWorkerDiskGB), body["disk"])
	require.Equal(t, "ssh_direct", body["runtype"])
	require.Equal(t, true, body["vm"])
	require.Equal(t, "machine-123", reservation.MachineID)
	require.Equal(t, "beam-workspace-training-machine-123", reservation.Name)
	require.Equal(t, testVastWorkerDiskGB*1024, reservation.StorageMB)
	require.Contains(t, body["onstart"], "#!/usr/bin/env bash")
	require.Contains(t, body["onstart"], "set -euo pipefail")
	require.Contains(t, body["onstart"], "sed -i")
	require.Contains(t, body["onstart"], `ssh-\(rsa\|ed25519\)`)
	require.Contains(t, body["onstart"], "/etc/environment")
	require.Contains(t, body["onstart"], "--join-token token")
}

func TestListOffersNormalizesVastConsumerGPUName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]any{"in": []any{"RTX 4090"}}, body["gpu_name"])
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}))
	defer server.Close()

	_, err := NewVast(testVastConfig(server.URL)).ListOffers(context.Background(), OfferRequest{GPUs: []string{"RTX4090"}})
	require.NoError(t, err)
}

func TestListOffersRejectsUndersizedVastWorkerDisks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offers":[{"id":123,"gpu_name":"RTX 4090","num_gpus":1,"dph_total":0.5,"disk_space":199.9,"rentable_count":1}]}`))
	}))
	defer server.Close()

	offers, err := NewVast(testVastConfig(server.URL)).ListOffers(context.Background(), OfferRequest{GPUs: []string{"RTX4090"}})
	require.NoError(t, err)
	require.Empty(t, offers)
}

func TestVastWorkerLaunchConfigurationIsRequired(t *testing.T) {
	client := NewVast(VastConfig{APIKey: "test-key"})

	_, err := client.ListOffers(context.Background(), OfferRequest{})
	require.EqualError(t, err, "vast worker image is required")

	client = NewVast(VastConfig{APIKey: "test-key", WorkerImage: testVastWorkerImage})
	_, err = client.ListOffers(context.Background(), OfferRequest{})
	require.EqualError(t, err, "vast worker disk must be greater than zero GB")
}

func TestGetReservationPreservesVastMappedSSHEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/instances/instance-123/", r.URL.Path)
		_, _ = w.Write([]byte(`{"instances":{
			"id":"instance-123",
			"gpu_name":"H100",
			"num_gpus":1,
			"disk_space":200,
			"public_ipaddr":"198.51.100.10",
			"ssh_host":"ssh4.vast.ai",
			"ssh_port":24567
		}}`))
	}))
	defer server.Close()

	reservation, err := NewVast(testVastConfig(server.URL)).GetReservation(context.Background(), "instance-123")
	require.NoError(t, err)
	require.Equal(t, "198.51.100.10", reservation.PublicIP)
	require.Equal(t, "ssh4.vast.ai", reservation.SSHHost)
	require.Equal(t, uint32(24567), reservation.SSHPort)
	require.Equal(t, int64(200*1024), reservation.StorageMB)
}
