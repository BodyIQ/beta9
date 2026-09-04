package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const VastDefaultBaseURL = "https://console.vast.ai/api/v0"

type VastClient struct {
	api          HTTPClient
	workerImage  string
	workerDiskGB int64
}

type VastConfig struct {
	APIKey       string
	BaseURL      string
	WorkerImage  string
	WorkerDiskGB int64
	Client       *http.Client
}

func NewVast(config VastConfig) *VastClient {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = VastDefaultBaseURL
	}
	return &VastClient{
		api: HTTPClient{
			BaseURL: baseURL,
			Token:   config.APIKey,
			Client:  config.Client,
		},
		workerImage:  strings.TrimSpace(config.WorkerImage),
		workerDiskGB: config.WorkerDiskGB,
	}
}

func (c *VastClient) Name() string {
	return "vast"
}

func (c *VastClient) validateWorkerConfig() error {
	if c.workerImage == "" {
		return fmt.Errorf("vast worker image is required")
	}
	if c.workerDiskGB <= 0 {
		return fmt.Errorf("vast worker disk must be greater than zero GB")
	}
	return nil
}

func (c *VastClient) ListOffers(ctx context.Context, req OfferRequest) ([]Offer, error) {
	if err := c.validateWorkerConfig(); err != nil {
		return nil, err
	}

	body := map[string]any{
		"type":              "on-demand",
		"rentable":          map[string]any{"eq": true},
		"verified":          map[string]any{"eq": true},
		"vms_enabled":       map[string]any{"eq": true},
		"disk_space":        map[string]any{"gte": c.workerDiskGB},
		"allocated_storage": c.workerDiskGB,
	}
	if len(req.GPUs) > 0 {
		body["gpu_name"] = map[string]any{"in": vastGPUQueryNames(req.GPUs)}
	}

	var raw map[string]any
	if err := c.api.Do(ctx, http.MethodPost, "/bundles/", body, &raw); err != nil {
		return nil, err
	}

	items := jsonArray(raw, "offers", "results", "bundles")
	if items == nil {
		if arr, ok := raw["data"].([]any); ok {
			items = arr
		}
	}

	offers := make([]Offer, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		offer := vastOfferFromMap(m)
		if offer.ID == "" || offer.GPUCount == 0 {
			continue
		}
		if offer.StorageMB < c.workerDiskGB*1024 {
			continue
		}
		if req.Nodes > 0 && offer.Available == 0 {
			offer.Available = 1
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

func vastGPUQueryNames(gpus []string) []string {
	names := make([]string, 0, len(gpus))
	for _, gpu := range gpus {
		switch NormalizeGPU(gpu) {
		case "A6000":
			names = append(names, "RTX A6000")
		case "RTX4090":
			names = append(names, "RTX 4090")
		case "RTX5090":
			names = append(names, "RTX 5090")
		default:
			names = append(names, gpu)
		}
	}
	return names
}

func (c *VastClient) CreateReservation(ctx context.Context, req ReservationRequest) (*Reservation, error) {
	if err := c.validateWorkerConfig(); err != nil {
		return nil, err
	}
	if req.Offer.ID == "" {
		return nil, fmt.Errorf("missing Vast offer id")
	}
	clientID := req.Selector
	if req.MachineID != "" {
		clientID = req.MachineID
	}
	body := map[string]any{
		"label":     ReservationNodeName(req),
		"client_id": clientID,
		"image":     c.workerImage,
		"disk":      c.workerDiskGB,
		"runtype":   "ssh_direct",
		"vm":        true,
	}
	if req.BootstrapCommand != "" {
		body["onstart"] = fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
# Vast's KVM cloud-init currently appends bare SSH public-key payloads to
# /etc/environment. They are not valid environment assignments and break apt
# package hooks that source the file during the agent's runtime installation.
if [ -f /etc/environment ]; then
  sed -i '/ssh-\(rsa\|ed25519\)[[:space:]]/d' /etc/environment
fi
%s
`, req.BootstrapCommand)
	}

	var raw map[string]any
	path := fmt.Sprintf("/asks/%s/", req.Offer.ID)
	if err := c.api.Do(ctx, http.MethodPut, path, body, &raw); err != nil {
		return nil, err
	}

	instanceID := jsonString(raw, "new_contract", "instance_id", "id")
	now := time.Now()
	return &Reservation{
		ID:               instanceID,
		PoolName:         req.PoolName,
		Name:             ReservationNodeName(req),
		Selector:         req.Selector,
		Provider:         c.Name(),
		Cloud:            reservationCloud(req.Offer.Cloud, c.Name()),
		Region:           req.Offer.Region,
		OfferID:          req.Offer.ID,
		InstanceType:     req.Offer.InstanceType,
		InstanceID:       instanceID,
		MachineID:        req.MachineID,
		GPU:              req.Offer.GPU,
		GPUCount:         req.Offer.GPUCount,
		NodeCount:        firstNonZeroUint32(req.Offer.NodeCount, 1),
		CPUMillicores:    req.Offer.CPUMillicores,
		MemoryMB:         req.Offer.MemoryMB,
		StorageMB:        c.workerDiskGB * 1024,
		HourlyCostMicros: req.Offer.HourlyCostMicros,
		CommittedMicros:  req.Offer.HourlyCostMicros * WholeHours(req.TTL),
		Source:           req.Source,
		Status:           ReservationPending,
		CreatedAt:        now,
		ExpiresAt:        now.Add(req.TTL),
		BillingRenewalAt: now.Add(time.Hour),
	}, nil
}

func (c *VastClient) GetReservation(ctx context.Context, id string) (*Reservation, error) {
	var raw map[string]any
	if err := c.api.Do(ctx, http.MethodGet, fmt.Sprintf("/instances/%s/", id), nil, &raw); err != nil {
		return nil, err
	}
	instance := raw
	if nested, ok := raw["instances"].(map[string]any); ok {
		instance = nested
	}
	offer := vastOfferFromMap(instance)
	return &Reservation{
		ID:               id,
		Provider:         c.Name(),
		Cloud:            reservationCloud(offer.Cloud, c.Name()),
		Region:           offer.Region,
		OfferID:          offer.ID,
		InstanceType:     offer.InstanceType,
		InstanceID:       id,
		GPU:              offer.GPU,
		GPUCount:         offer.GPUCount,
		NodeCount:        firstNonZeroUint32(offer.NodeCount, 1),
		CPUMillicores:    offer.CPUMillicores,
		MemoryMB:         offer.MemoryMB,
		StorageMB:        offer.StorageMB,
		HourlyCostMicros: offer.HourlyCostMicros,
		Status:           ReservationActive,
		PublicIP:         jsonString(instance, "public_ipaddr", "public_ip"),
		SSHHost:          jsonString(instance, "ssh_host"),
		SSHPort:          jsonPort(instance, "ssh_port"),
	}, nil
}

func reservationCloud(cloud, fallback string) string {
	if cloud != "" {
		return cloud
	}
	return fallback
}

// ExtendReservation is a no-op: Vast has no provider-side auto-delete
// threshold; reservation lifetime is enforced by our reconciler only.
func (c *VastClient) ExtendReservation(ctx context.Context, id string, expiresAt time.Time) error {
	return nil
}

func (c *VastClient) DeleteReservation(ctx context.Context, id string) error {
	return c.api.Do(ctx, http.MethodDelete, fmt.Sprintf("/instances/%s/", id), nil, nil)
}

func vastOfferFromMap(m map[string]any) Offer {
	raw, _ := json.Marshal(m)
	id := jsonString(m, "id", "ask_contract_id", "bundle_id")
	if id == "" {
		if value := jsonInt64(m, "id", "ask_contract_id"); value > 0 {
			id = strconv.FormatInt(value, 10)
		}
	}
	gpuCount := uint32(jsonInt64(m, "num_gpus", "gpu_count", "gpus"))
	hourlyCost := jsonFloat64(m, "dph_total", "price", "hourly_cost", "cost_per_hour")
	return Offer{
		ID:               id,
		Provider:         "vast",
		Cloud:            "vast",
		InstanceType:     jsonString(m, "machine_id", "instance_type", "hostname"),
		Region:           jsonString(m, "geolocation", "region", "location"),
		GPU:              NormalizeGPU(jsonString(m, "gpu_name", "gpu", "gpu_type")),
		GPUCount:         gpuCount,
		NodeCount:        1,
		CPUMillicores:    int64(jsonFloat64(m, "cpu_cores", "vcpus", "cpu") * 1000),
		MemoryMB:         int64(jsonFloat64(m, "cpu_ram", "memory_mb", "ram") * 1024),
		StorageMB:        int64(jsonFloat64(m, "disk_space", "storage_gb", "disk_gb") * 1024),
		HourlyCostMicros: DollarsToMicros(hourlyCost),
		Reliability:      jsonFloat64(m, "reliability2", "reliability", "score"),
		Available:        uint32(jsonInt64(m, "available", "availability", "rentable_count")),
		Raw:              raw,
	}
}
